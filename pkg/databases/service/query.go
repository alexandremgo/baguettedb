package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// safeTableName gates table names on read paths: the admission charset and length cap, plus a leading `_`
// so platform-reserved tables stay describable (reads on them are deliberately allowed).
var safeTableName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,127}$`)

// QueryService handles SQL query execution against bdb databases.
type QueryService struct {
	dbSvc           *DatabaseService
	executor        queryExecutor
	classifier      statementClassifier
	quotaResolver   quota.Resolver
	affectedRowsMax int
	requestTimeout  time.Duration
	gate            *inFlightGate
}

// NewQueryService creates a new QueryService. requestTimeout bounds one request's classification and execution,
// including any wait for the database's connection; a non-positive value disables the budget.
func NewQueryService(
	dbSvc *DatabaseService,
	executor queryExecutor,
	classifier statementClassifier,
	quotaResolver quota.Resolver,
	affectedRowsMax int,
	requestTimeout time.Duration,
) *QueryService {
	if affectedRowsMax <= 0 {
		affectedRowsMax = domain.DefaultAffectedRowsMax
	}
	return &QueryService{
		dbSvc:           dbSvc,
		executor:        executor,
		classifier:      classifier,
		quotaResolver:   quotaResolver,
		affectedRowsMax: affectedRowsMax,
		requestTimeout:  requestTimeout,
		gate:            newInFlightGate(),
	}
}

// NewQueryServiceForTesting builds a QueryService for tests that exercise
// the SQL-execution paths without the heavy DatabaseService wiring
// (Postgres repo, KEK provider, database manager, bdbs app). The
// embedded DatabaseService carries only the logger — paths that touch
// other fields will panic, so this is reserved for tests that drive
// QueryService methods directly.
func NewQueryServiceForTesting(
	executor queryExecutor,
	classifier statementClassifier,
	quotaResolver quota.Resolver,
	logger zerolog.Logger,
) *QueryService {
	return &QueryService{
		dbSvc:           &DatabaseService{logger: logger},
		executor:        executor,
		classifier:      classifier,
		quotaResolver:   quotaResolver,
		affectedRowsMax: domain.DefaultAffectedRowsMax,
		gate:            newInFlightGate(),
	}
}

// Limits returns the resource limits that apply to the caller identified by ctx's roles.
func (s *QueryService) Limits(ctx context.Context) quota.Limits {
	return s.quotaResolver(authz.RolesFromContext(ctx))
}

// Execute classifies the SQL statement, validates it against the endpoint intent,
// derives the required permission, and executes it against the bdb's database.
// args binds the single statement's placeholders; bound args combined with
// multi-statement input are rejected (a flat slice has no per-statement meaning).
func (s *QueryService) Execute(
	ctx context.Context,
	userID, bdbID uuid.UUID,
	sql string,
	args []domain.TypedValue,
	intent domain.OperationIntent,
) (*domain.QueryResult, error) {
	var argsPerStmt [][]domain.TypedValue
	if len(args) > 0 {
		argsPerStmt = [][]domain.TypedValue{args}
	}
	return s.execute(ctx, userID, bdbID, sql, argsPerStmt, intent)
}

// ExecuteStatements is Execute for pre-bound multi-statement input: argsPerStmt[i] binds
// statement i's placeholders, so a request may carry N >= 1 statements each with its own
// arguments. Classification, intent validation, caps, permission checks, and the change-log
// path are shared with Execute.
func (s *QueryService) ExecuteStatements(
	ctx context.Context,
	userID, bdbID uuid.UUID,
	sql string,
	argsPerStmt [][]domain.TypedValue,
	intent domain.OperationIntent,
) (*domain.QueryResult, error) {
	return s.execute(ctx, userID, bdbID, sql, argsPerStmt, intent)
}

// execute runs the shared authorize → resolve → classify → execute path with per-statement bound args.
//
// Preflight order:
//  0. Take one of the caller's in-flight slots; at the cap the request is refused with QueryRateLimitedError
//     before it costs anything.
//  1. Authorize membership (any-role gate): a non-member gets ErrAccessDenied before anything is provisioned
//     or read, so they can neither create the bdb's database on the owner's storage quota nor classify SQL
//     against its schema.
//  2. Resolve the bdb's database (lazy provisioning, pending platform migrations).
//  3. Acquire the connection and classify the SQL against its schema.
//  4. Validate intent + WITHOUT ROWID rejection (DDL only).
//  5. Check the operation-specific permission (now that we know the statement type).
//  6. Execute on the same database.
//
// Steps 3 to 6 run under the request time budget and surface ErrQueryTimeout when it fires; bookkeeping that follows
// a committed transaction is detached from the budget so a late deadline never drops it.
//
// Reserved-prefix protection is enforced by the SQLite authorizer hook during step 3's classify (writing to
// `_*` tables fails at parse time), so no separate validateReservedTables call is needed here.
func (s *QueryService) execute(
	ctx context.Context,
	userID, bdbID uuid.UUID,
	sql string,
	argsPerStmt [][]domain.TypedValue,
	intent domain.OperationIntent,
) (*domain.QueryResult, error) {
	ctx, span := tracer.Start(ctx, "QueryService.Execute",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.Int("intent", int(intent)),
		),
	)
	defer span.End()

	ctx, release, err := s.acquireSlot(ctx, userID)
	if err != nil {
		return nil, err
	}
	defer release()

	if err := s.dbSvc.AuthorizeMembership(ctx, userID, bdbID); err != nil {
		return nil, err
	}
	db, err := s.dbSvc.ResolveBdbDatabase(ctx, bdbID)
	if err != nil {
		return nil, err
	}

	runCtx := ctx
	if s.requestTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, s.requestTimeout)
		defer cancel()
	}
	result, err := s.classifyAndRun(runCtx, userID, bdbID, db, sql, argsPerStmt, intent)
	if err != nil {
		// Attributes the failure to the budget only when it fired while the caller's own ctx was still live: a client
		// disconnect or an inherited shorter deadline must not masquerade as the query timeout.
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("%w (limit %s)", domain.ErrQueryTimeout, s.requestTimeout)
		}
		return nil, err
	}
	return result, nil
}

// classifyAndRun classifies sql on db's connection, applies the intent, admission and permission checks, and executes
// it. ctx carries the request's time budget; on expiry the adapter interrupts SQLite and the error propagates as-is.
func (s *QueryService) classifyAndRun(
	ctx context.Context,
	userID, bdbID uuid.UUID,
	db *domain.Database,
	sql string,
	argsPerStmt [][]domain.TypedValue,
	intent domain.OperationIntent,
) (*domain.QueryResult, error) {
	databaseID := db.ID.String()

	// Classifies on the database's connection so SQLite's parser
	// resolves against the right schema and the authorizer enforces
	// reserved-prefix protection at parse time.
	//
	// All classification happens here in one prepare-only walk, before
	// the user's SQL executes. Re-classifying afterwards would fail
	// because the post-execute schema differs (e.g. CREATE TABLE makes
	// the table exist, breaking a second prepare).
	//
	// The connection's own failures (opening the file, a cancelled request) pass through as they are: only the
	// classifier's errors describe the SQL.
	var (
		stmts       []domain.ClassifiedStatement
		classifyErr error
	)
	if err := s.executor.WithConn(ctx, databaseID, func(h ParseHandle) error {
		stmts, classifyErr = s.classifier.Classify(h, sql)
		return nil
	}); err != nil {
		return nil, err
	}
	if classifyErr != nil {
		// Reserved-prefix denials (ErrReservedTable) and SQL SQLite cannot prepare (ErrInvalidSQL) are already typed
		// by the classifier; pass them through. Other parse errors become ErrUnsupportedStatement.
		if errors.Is(classifyErr, domain.ErrReservedTable) || errors.Is(classifyErr, domain.ErrInvalidSQL) {
			return nil, classifyErr
		}
		return nil, fmt.Errorf("%w: %w", domain.ErrUnsupportedStatement, classifyErr)
	}

	if err := validateIntentForStatements(intent, stmts); err != nil {
		return nil, err
	}
	if len(argsPerStmt) == 0 {
		if err := domain.MissingParameters(stmts); err != nil {
			return nil, err
		}
	}

	// Statement-count cap (parse-time): rejects oversized requests
	// before any DB write. Resolved per-request from the user's roles so
	// downstream distributions can raise the cap per role.
	roles := authz.RolesFromContext(ctx)
	maxStmts := s.quotaResolver(roles).MaxStatementsPerRequest
	if maxStmts > 0 && len(stmts) > maxStmts {
		return nil, fmt.Errorf("%w: exceeded max statements (%d); request has %d",
			domain.ErrTooManyStatements, maxStmts, len(stmts))
	}

	headType := stmts[0].Type

	// Args must map one slice per statement (placeholders restart at 1 in each statement). A
	// single flat slice against N>1 is rejected explicitly so it isn't silently bound to the
	// first statement; any other count mismatch is a caller bug surfaced verbatim.
	if len(argsPerStmt) > 0 && len(argsPerStmt) != len(stmts) {
		if len(argsPerStmt) == 1 {
			return nil, fmt.Errorf("%w: bound arguments are not supported with multi-statement input",
				domain.ErrUnsupportedStatement)
		}
		return nil, fmt.Errorf("%w: got arguments for %d statements, input has %d",
			domain.ErrUnsupportedStatement, len(argsPerStmt), len(stmts))
	}

	// DDL admission checks: every created object name must be a valid user identifier, and WITHOUT
	// ROWID tables are rejected anywhere in the request (change log triggers depend on implicit
	// rowid). ALTER rename/add targets aren't visible at prepare time and are validated on the DDL
	// execution path instead.
	if headType == domain.StatementDDL {
		for i, cs := range stmts {
			if err := validateDDLAdmission(cs); err != nil {
				if len(stmts) > 1 {
					return nil, fmt.Errorf("statement %d: %w", i+1, err)
				}
				return nil, err
			}
		}
	}

	perm, err := requiredPermission(headType)
	if err != nil {
		return nil, err
	}
	if err := s.dbSvc.CheckDatabasePermission(ctx, userID, bdbID, perm); err != nil {
		return nil, err
	}

	if readOnlyBlocks(db, perm) {
		return nil, domain.ErrDatabaseReadOnly
	}

	// Aggregate storage quota: rejects size-growing batches when the bdb owner's total tracked storage
	// is at its cap. Pure deletions and DQL stay allowed so an over-budget owner can always recover.
	if (headType == domain.StatementDML || headType == domain.StatementDDL) &&
		domain.StatementsGrowStorage(stmts) {
		if err := s.dbSvc.CheckOwnerStorageQuota(ctx, bdbID); err != nil {
			return nil, err
		}
	}

	// DML: wraps in transaction with changed_by correction. Supports N>=1 statements.
	if headType == domain.StatementDML {
		sqlStmts := make([]domain.SQLStatement, len(stmts))
		for i, cs := range stmts {
			sqlStmts[i] = domain.SQLStatement{SQL: cs.SQL}
			if len(argsPerStmt) > 0 {
				sqlStmts[i].Args = argsPerStmt[i]
			}
		}
		maxRows := s.quotaResolver(roles).MaxRowsPerRequest
		result, err := s.executeDMLWithChangeLog(ctx, databaseID, userID, domain.ChangeOriginChange, sqlStmts, maxRows)
		if err != nil {
			return nil, err
		}
		// The transaction has committed: retention sweeps must not be dropped by a budget that fires right after it.
		postCommitCtx := context.WithoutCancel(ctx)
		s.cleanupChangeLog(postCommitCtx, databaseID)
		s.cleanupStack(postCommitCtx, databaseID, domain.UndoStackTable, userID)
		return result, nil
	}

	// DDL: routes through one transaction with pre-image capture and
	// deferred trigger regeneration. Same path for N=1 and N>1 — each
	// touched table regen'd once at end-of-tx from its post-DDL schema
	// (no prediction; the txn isolates concurrent writers).
	if headType == domain.StatementDDL {
		result, _, err := s.executeDDLRequest(ctx, databaseID, userID, stmts)
		if err != nil {
			return nil, err
		}
		s.cleanupChangeLog(context.WithoutCancel(ctx), databaseID)
		return result, nil
	}

	// Below here: N=1 DQL. The result-row cap fails loud rather than truncating: a truncated page would read as
	// "that's all the data" to an assistant.
	single := stmts[0]
	var args []domain.TypedValue
	if len(argsPerStmt) > 0 {
		args = argsPerStmt[0]
	}
	maxRows := s.quotaResolver(roles).MaxQueryResultRows
	result, err := s.executor.ExecuteWithRowCap(ctx, databaseID, single.SQL, args, maxRows)
	if errors.Is(err, domain.ErrQueryResultTooLarge) {
		return nil, fmt.Errorf("%w (limit %d rows)", err, maxRows)
	}
	return result, err
}

// validateIntentForStatements checks that every classified statement matches the request's intent: a single DQL
// statement for IntentQuery, DML only for IntentExecute, DDL only for IntentSchema. A mismatch returns a
// *domain.StatementKindError, numbering the refused statement when the request holds several.
func validateIntentForStatements(intent domain.OperationIntent, stmts []domain.ClassifiedStatement) error {
	if len(stmts) == 0 {
		// Classify already errors on empty input; defensive guard.
		return fmt.Errorf("%w: empty SQL", domain.ErrUnsupportedStatement)
	}
	if intent == domain.IntentQuery && len(stmts) > 1 {
		return &domain.StatementKindError{Intent: intent, Count: len(stmts)}
	}
	for i, s := range stmts {
		if s.Type == intentStatementType(intent) {
			continue
		}
		refused := &domain.StatementKindError{Intent: intent, Kind: s.Type}
		if len(stmts) > 1 {
			refused.Statement = i + 1
		}
		return refused
	}
	return nil
}

// intentStatementType returns the statement type intent runs.
func intentStatementType(intent domain.OperationIntent) domain.StatementType {
	switch intent {
	case domain.IntentQuery:
		return domain.StatementDQL
	case domain.IntentExecute:
		return domain.StatementDML
	case domain.IntentSchema:
		return domain.StatementDDL
	default:
		return domain.StatementUnsupported
	}
}

// ListTables returns all user-created tables and views in the bdb's database.
func (s *QueryService) ListTables(ctx context.Context, userID, bdbID uuid.UUID) (*domain.QueryResult, error) {
	ctx, span := tracer.Start(ctx, "QueryService.ListTables",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()
	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}

	infos, err := s.listUserTables(ctx, db.ID.String())
	if err != nil {
		return nil, err
	}

	rows := make([][]domain.TypedValue, 0, len(infos))
	for _, info := range infos {
		rows = append(rows, []domain.TypedValue{
			domain.TextValue(info.Name), domain.TextValue(info.Type), domain.TextValue(info.Module),
		})
	}
	return &domain.QueryResult{
		Columns: []domain.Column{
			{Name: "name", DeclaredType: "TEXT"}, {Name: "type", DeclaredType: "TEXT"},
			{Name: "module", DeclaredType: "TEXT"},
		},
		Rows: rows,
	}, nil
}

// listUserTables reads `pragma_table_list` for an already-authorized database and returns the user-visible
// table list. SQLite classifies each object as table / view / virtual / shadow itself (shadow via the
// virtual-table module's xShadowName hook), so shadow tables — index internals like fts5's `<vt>_data` — are
// excluded here without any name heuristics. For virtual tables, the module name (e.g. "fts5") is extracted
// from the joined sqlite_master CREATE statement, the only place SQLite records it.
func (s *QueryService) listUserTables(ctx context.Context, databaseID string) ([]domain.TableInfo, error) {
	listSQL := `SELECT tl.name, tl.type, m.sql FROM pragma_table_list AS tl ` +
		`LEFT JOIN sqlite_master AS m ON m.name = tl.name ` +
		`WHERE tl.schema = 'main' AND tl.type IN ('table', 'view', 'virtual') ` +
		`AND tl.name NOT LIKE 'sqlite_%' AND tl.name NOT LIKE '\_%' ESCAPE '\'`
	listResult, err := s.executor.Execute(ctx, databaseID, listSQL, nil)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}

	infos := make([]domain.TableInfo, 0, len(listResult.Rows))
	for _, row := range listResult.Rows {
		if len(row) < 3 {
			continue
		}
		name, _ := row[0].Value.(string)
		tblType, _ := row[1].Value.(string)
		if name == "" {
			continue
		}
		module := ""
		if tblType == "virtual" {
			createSQL, _ := row[2].Value.(string)
			module = domain.ModuleFromCreateSQL(createSQL)
		}
		infos = append(infos, domain.TableInfo{Name: name, Type: tblType, Module: module})
	}
	return infos, nil
}

// DescribeTable returns column and foreign key information for the given table.
func (s *QueryService) DescribeTable(
	ctx context.Context, userID, bdbID uuid.UUID, tableName string,
) (*domain.TableDescription, error) {
	ctx, span := tracer.Start(ctx, "QueryService.DescribeTable",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("table.name", tableName),
		),
	)
	defer span.End()
	if !safeTableName.MatchString(tableName) {
		return nil, fmt.Errorf("%w: invalid table name %q", domain.ErrQueryExecution, tableName)
	}

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}

	return s.describeTableInDatabase(ctx, db.ID.String(), tableName)
}

// ListTablesInfo returns the lightweight {name, type} list of user-created
// tables and views for the given bdb. Used by callers that don't need
// column-level schemas (cross-module orientation tools); for full schemas use
// ListTablesWithSchemas.
func (s *QueryService) ListTablesInfo(ctx context.Context, userID, bdbID uuid.UUID) ([]domain.TableInfo, error) {
	ctx, span := tracer.Start(ctx, "QueryService.ListTablesInfo",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}

	return s.listUserTables(ctx, db.ID.String())
}

// ListTablesWithSchemas returns all user-created tables with their full schema
// (columns and foreign keys) for the given bdb.
func (s *QueryService) ListTablesWithSchemas(
	ctx context.Context, userID, bdbID uuid.UUID,
) ([]domain.TableSchema, error) {
	ctx, span := tracer.Start(ctx, "QueryService.ListTablesWithSchemas",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}

	databaseID := db.ID.String()

	infos, err := s.listUserTables(ctx, databaseID)
	if err != nil {
		return nil, err
	}

	schemas := make([]domain.TableSchema, 0, len(infos))
	for _, info := range infos {
		describeError := ""
		desc, err := s.describeTableInDatabase(ctx, databaseID, info.Name)
		if err != nil {
			// A failed introspection degrades the entry instead of dropping it or faking an empty
			// schema — consumers must be able to tell "unreadable" from "has no columns".
			s.dbSvc.logger.Warn().Err(err).Str("table", info.Name).
				Msg("Failed to describe table in ListTablesWithSchemas")
			desc = &domain.TableDescription{}
			describeError = fmt.Sprintf("schema introspection failed: %v", err)
		}

		schemas = append(schemas, domain.TableSchema{
			Name:          info.Name,
			Type:          info.Type,
			Module:        info.Module,
			Schema:        *desc,
			DescribeError: describeError,
		})
	}

	return schemas, nil
}

// describeTableInDatabase returns column and foreign key information for a table
// within an already-authorized database.
//
// The introspection reads bind tableName as a parameter of the pragma table-valued functions, so names
// that would break a string-interpolated PRAGMA (embedded quotes especially) stay plain data.
func (s *QueryService) describeTableInDatabase(
	ctx context.Context, databaseID, tableName string,
) (*domain.TableDescription, error) {
	nameArg := []domain.TypedValue{domain.TextValue(tableName)}
	colResult, err := s.executor.Execute(ctx, databaseID, "SELECT * FROM pragma_table_info(?)", nameArg)
	if err != nil {
		return nil, fmt.Errorf("reading table_info: %w", err)
	}

	fkResult, err := s.executor.Execute(ctx, databaseID, "SELECT * FROM pragma_foreign_key_list(?)", nameArg)
	if err != nil {
		return nil, fmt.Errorf("reading foreign_key_list: %w", err)
	}

	kind, module, createSQL := s.lookupTableMeta(ctx, databaseID, tableName)

	desc := &domain.TableDescription{
		Columns:     parseTableColumns(colResult),
		ForeignKeys: parseForeignKeys(fkResult),
		Kind:        kind,
		Module:      module,
	}
	// CHECK constraints exist only on regular tables, and the stored CREATE statement is the only
	// introspection source for them — no pragma carries them.
	if kind == "table" {
		desc.Checks = domain.CheckConstraintsFromCreateSQL(createSQL)
	}
	return desc, nil
}

// lookupTableMeta reads `pragma_table_list` for the named object and returns its SQLite-classified kind
// ("table" / "view" / "virtual" / "shadow"), the virtual-table module name (e.g. "fts5") when the kind is
// "virtual", and the stored CREATE statement from the joined sqlite_master row. Best-effort: on any read
// failure or missing row it returns ("table", "", "") — describe_table still works, the agent just loses
// the kind/module/constraint hints.
func (s *QueryService) lookupTableMeta(ctx context.Context, databaseID, tableName string) (string, string, string) {
	const sql = `SELECT tl.type, m.sql FROM pragma_table_list AS tl ` +
		`LEFT JOIN sqlite_master AS m ON m.name = tl.name ` +
		`WHERE tl.schema = 'main' AND tl.name = ? LIMIT 1`
	args := []domain.TypedValue{domain.TextValue(tableName)}
	result, err := s.executor.Execute(ctx, databaseID, sql, args)
	if err != nil || result == nil || len(result.Rows) == 0 {
		return "table", "", ""
	}
	row := result.Rows[0]
	if len(row) < 2 {
		return "table", "", ""
	}
	kind, _ := row[0].Value.(string)
	if kind == "" {
		return "table", "", ""
	}
	createSQL, _ := row[1].Value.(string)
	module := ""
	if kind == "virtual" {
		module = domain.ModuleFromCreateSQL(createSQL)
	}
	return kind, module, createSQL
}

// parseTableColumns converts a PRAGMA table_info result into structured TableColumn values.
// PRAGMA table_info columns: cid(0), name(1), type(2), notnull(3), dflt_value(4), pk(5).
func parseTableColumns(result *domain.QueryResult) []domain.TableColumn {
	if result == nil || len(result.Rows) == 0 {
		return []domain.TableColumn{}
	}

	cols := make([]domain.TableColumn, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) < 6 {
			continue
		}
		name, _ := row[1].Value.(string)
		colType, _ := row[2].Value.(string)
		notNull := toInt64(row[3]) != 0
		dflt := fmt.Sprintf("%v", row[4].Value)
		if row[4].Value == nil {
			dflt = ""
		}
		pk := toInt64(row[5]) != 0

		cols = append(cols, domain.TableColumn{
			Name:       name,
			Type:       colType,
			NotNull:    notNull,
			Default:    dflt,
			PrimaryKey: pk,
		})
	}
	return cols
}

// parseForeignKeys converts a PRAGMA foreign_key_list result into structured ForeignKey values.
// PRAGMA foreign_key_list columns: id(0), seq(1), table(2), from(3), to(4), on_update(5), on_delete(6), match(7).
func parseForeignKeys(result *domain.QueryResult) []domain.ForeignKey {
	if result == nil || len(result.Rows) == 0 {
		return []domain.ForeignKey{}
	}

	fks := make([]domain.ForeignKey, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) < 7 {
			continue
		}
		table, _ := row[2].Value.(string)
		from, _ := row[3].Value.(string)
		to, _ := row[4].Value.(string)
		onUpdate, _ := row[5].Value.(string)
		onDelete, _ := row[6].Value.(string)

		fks = append(fks, domain.ForeignKey{
			From:     from,
			Table:    table,
			To:       to,
			OnUpdate: onUpdate,
			OnDelete: onDelete,
		})
	}
	return fks
}

// toInt64 extracts an int64 from a TypedValue, returning 0 if the value is not an integer.
func toInt64(v domain.TypedValue) int64 {
	if n, ok := v.Value.(int64); ok {
		return n
	}
	return 0
}

// ListChanges returns recent _change_log entries grouped by batch. Reads the unified _change_log table and returns
// both DML and DDL entries. Filters compose: an empty kind returns both; tableName narrows to a single table (DML
// rows + DDL rows whose object_name matches); filterUserID narrows to one author. limit caps the row count (default
// 20, max 100).
func (s *QueryService) ListChanges(
	ctx context.Context,
	userID, bdbID uuid.UUID,
	kind domain.ChangeKind,
	tableName string,
	filterUserID *uuid.UUID,
	limit int,
) (*domain.ListChangesResult, error) {
	ctx, span := tracer.Start(ctx, "QueryService.ListChanges",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("kind", string(kind)),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}

	databaseID := db.ID.String()

	if limit <= 0 || limit > 100 {
		limit = 20
	}

	conditions := []string{}
	args := []domain.TypedValue{}
	if kind != "" {
		conditions = append(conditions, "kind = ?")
		args = append(args, domain.TextValue(string(kind)))
	}
	if tableName != "" {
		// DML rows use table_name; DDL rows use object_name. Match either.
		conditions = append(conditions, "(table_name = ? OR object_name = ?)")
		args = append(args, domain.TextValue(tableName), domain.TextValue(tableName))
	}
	if filterUserID != nil {
		conditions = append(conditions, "changed_by = ?")
		args = append(args, domain.TextValue(filterUserID.String()))
	}
	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}
	fetchSQL := fmt.Sprintf("SELECT %s FROM %s%s ORDER BY id DESC LIMIT ?",
		changeLogFullColumns, domain.ChangeLogTable, whereClause)
	args = append(args, domain.IntegerValue(int64(limit)))

	result, err := s.executor.Execute(ctx, databaseID, fetchSQL, args)
	if err != nil {
		return nil, err
	}

	batchOrder := []string{}
	batchMap := map[string]*domain.ChangeBatch{}
	for _, row := range result.Rows {
		entry := parseChangeLogFullRow(row)
		batch, exists := batchMap[entry.BatchID]
		if !exists {
			batch = &domain.ChangeBatch{
				BatchID:   entry.BatchID,
				Kind:      entry.Kind,
				Origin:    entry.Origin,
				ChangedBy: entry.ChangedBy,
				ChangedAt: entry.ChangedAt,
			}
			batchMap[entry.BatchID] = batch
			batchOrder = append(batchOrder, entry.BatchID)
		}
		batch.Entries = append(batch.Entries, *entry)
	}
	batches := make([]domain.ChangeBatch, len(batchOrder))
	for i, id := range batchOrder {
		batches[i] = *batchMap[id]
	}
	return &domain.ListChangesResult{Batches: batches}, nil
}

// RevertChange undoes a single _change_log entry — DML or DDL — by executing its inverse and recording it as
// forward rows with origin='revert'. On conflict the call returns a *ConflictError carrying the structured payload,
// which errors.AsType[*ConflictError] extracts.
//
// The entry fetch, the conflict check, and the inverse run inside one transaction, so no write can land between the
// check and the apply.
func (s *QueryService) RevertChange(
	ctx context.Context,
	userID, bdbID uuid.UUID,
	changeLogID int64,
) (*domain.QueryResult, error) {
	ctx, span := tracer.Start(ctx, "QueryService.RevertChange",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.Int64("change_log.id", changeLogID),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermWrite)
	if err != nil {
		return nil, err
	}

	return s.revert(ctx, db.ID.String(), userID, func(
		ctx context.Context, exec internalExecutor, databaseID string,
	) ([]*domain.ChangeLogEntry, error) {
		entry, err := s.fetchChangeLogEntry(ctx, exec, databaseID, changeLogID)
		if err != nil {
			return nil, err
		}
		return []*domain.ChangeLogEntry{entry}, nil
	})
}

// RevertChangeBatch reverts all entries in a batch (DML or DDL). The batch fetch, the conflict check, and the inverse
// statements run inside one transaction and produce a fresh revert batch on the undo stack. Returns a *ConflictError
// on conflict.
func (s *QueryService) RevertChangeBatch(
	ctx context.Context,
	userID, bdbID uuid.UUID,
	batchID string,
) (*domain.QueryResult, error) {
	ctx, span := tracer.Start(ctx, "QueryService.RevertChangeBatch",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("source_batch_id", batchID),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermWrite)
	if err != nil {
		return nil, err
	}

	return s.revert(ctx, db.ID.String(), userID, func(
		ctx context.Context, exec internalExecutor, databaseID string,
	) ([]*domain.ChangeLogEntry, error) {
		entries, err := s.fetchChangeLogBatch(ctx, exec, databaseID, batchID)
		// The caller named a batch the log doesn't hold: their error to see, not an operational warning.
		if errors.Is(err, domain.ErrChangeLogEntryNotFound) {
			s.dbSvc.logger.Info().
				Str("database_id", databaseID).
				Str("source_batch_id", batchID).
				Msg("Revert refused: source batch has no _change_log rows (pruned by retention or never written)")
		}
		return entries, err
	})
}

// UndoLastChange pops the calling user's most-recent _undo_stack entry, loads its _change_log entries, validates
// that they are still reversible, then applies the inverse — all inside one transaction, so no write can land
// between the check and the apply. On success the same batch is pushed onto the redo stack.
//
// Returns a *ConflictError when the state has moved since the change was recorded; callers extract the structured
// payload via errors.AsType[*ConflictError].
func (s *QueryService) UndoLastChange(
	ctx context.Context, userID, bdbID uuid.UUID,
) (*domain.QueryResult, error) {
	ctx, span := tracer.Start(ctx, "QueryService.UndoLastChange",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermWrite)
	if err != nil {
		return nil, err
	}
	return s.undo(ctx, db.ID.String(), userID, s.userStackPeek(userID),
		"Undo refused: stack pointed at a _change_log batch that no longer has rows (stale stack)")
}

// RedoLastChange re-applies the most recently undone change. For DML batches, redo reverts the revert: the undo's
// trigger-written rows describe the inverse, so reverting them lands back at the original direction. For DDL
// batches, redo re-executes the original DDL statements through the normal DDL flow, which is symmetric: capture
// pre-image, run, write fresh _change_log + snapshot rows. A new user write clears the redo stack — redo only works
// immediately after undo.
func (s *QueryService) RedoLastChange(
	ctx context.Context, userID, bdbID uuid.UUID,
) (*domain.QueryResult, error) {
	ctx, span := tracer.Start(ctx, "QueryService.RedoLastChange",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermWrite)
	if err != nil {
		return nil, err
	}
	return s.redo(ctx, db.ID.String(), userID, s.userStackPeek(userID),
		"Redo refused: stack pointed at a _change_log batch that no longer has rows (stale stack)")
}

// UndoLastChangeAnyone reverses the most recent change in the bdb regardless of who made it. Requires
// db:manage_history (owner-only). The reversed batch lands on the *caller's* redo stack — the caller may not be the
// original author. Same kind-dispatching semantics as UndoLastChange.
func (s *QueryService) UndoLastChangeAnyone(
	ctx context.Context, userID, bdbID uuid.UUID,
) (*domain.QueryResult, error) {
	ctx, span := tracer.Start(ctx, "QueryService.UndoLastChangeAnyone",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermManageHistory)
	if err != nil {
		return nil, err
	}
	return s.undo(ctx, db.ID.String(), userID, s.peekStackAnyone,
		"Undo-anyone refused: stack pointed at a _change_log batch that no longer has rows (stale stack)")
}

// RedoLastChangeAnyone re-applies the most recently undone change in the bdb regardless of who undid it. Requires
// db:manage_history (owner-only). The re-applied batch lands on the *caller's* undo stack. Same DML-revert /
// DDL-re-execute split as RedoLastChange.
func (s *QueryService) RedoLastChangeAnyone(
	ctx context.Context, userID, bdbID uuid.UUID,
) (*domain.QueryResult, error) {
	ctx, span := tracer.Start(ctx, "QueryService.RedoLastChangeAnyone",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermManageHistory)
	if err != nil {
		return nil, err
	}
	return s.redo(ctx, db.ID.String(), userID, s.peekStackAnyone,
		"Redo-anyone refused: stack pointed at a _change_log batch that no longer has rows (stale stack)")
}

// --- Reversal transaction ----------------------------------------------

// entryLoader selects the change-log entries a revert targets, reading through the executor it is handed so the
// selection runs inside the reversal transaction.
type entryLoader func(ctx context.Context, exec internalExecutor, databaseID string) ([]*domain.ChangeLogEntry, error)

// stackPeek returns the position and batch id of the entry on top of stackTable that a reversal consumes, reading
// through the executor it is handed.
type stackPeek func(ctx context.Context, exec internalExecutor, databaseID, stackTable string) (int64, string, error)

// userStackPeek peeks the entries userID pushed.
func (s *QueryService) userStackPeek(userID uuid.UUID) stackPeek {
	return func(ctx context.Context, exec internalExecutor, databaseID, stackTable string) (int64, string, error) {
		return s.peekStack(ctx, exec, databaseID, stackTable, userID)
	}
}

// reversalOutcome is what a committed reversal transaction hands to the post-commit work: the batch the reversal's
// rows were written under and how many row changes it reversed (schema changes not counted).
type reversalOutcome struct {
	batchID string
	rows    int
}

// revert reverses the entries load selects. The load, the conflict check, and the inverse run inside one transaction;
// the reversal is recorded under a fresh batch with origin revert, which lands on the caller's undo stack — capped
// after the commit like any other push — while the caller's redo stack is cleared.
func (s *QueryService) revert(
	ctx context.Context, databaseID string, userID uuid.UUID, load entryLoader,
) (*domain.QueryResult, error) {
	revertBatchID := uuid.New().String()
	moves := []domain.SQLStatement{redoStackClearStmt(userID), undoStackPushStmt(revertBatchID, userID)}
	var outcome reversalOutcome
	err := s.executor.WithTx(ctx, databaseID, true, func(ctx context.Context, tx Tx) error {
		exec := txQueryExecutor{tx}
		entries, err := load(ctx, exec, databaseID)
		if err != nil {
			return err
		}
		if err := s.checkReversible(ctx, exec, databaseID, entries); err != nil {
			return err
		}
		recordReversalSpan(ctx, databaseID, revertBatchID, entries)
		outcome, err = s.applyReversalInTx(ctx, tx, databaseID, entries, userID, revertBatchID,
			domain.ChangeOriginRevert, moves)
		return err
	})
	if err != nil {
		return nil, err
	}
	result := s.finishReversal(ctx, databaseID, outcome)
	s.cleanupStack(ctx, databaseID, domain.UndoStackTable, userID)
	return result, nil
}

// undo reverses the batch on top of the undo stack peek selects. The peek, the batch load, the conflict check, and
// the inverse run inside one transaction; the reversal is recorded under a fresh batch with origin undo, which
// replaces the consumed entry on the caller's redo stack. refusal is the log line written when the stack points at a
// batch that has no rows left.
func (s *QueryService) undo(
	ctx context.Context, databaseID string, userID uuid.UUID, peek stackPeek, refusal string,
) (*domain.QueryResult, error) {
	undoBatchID := uuid.New().String()
	var outcome reversalOutcome
	err := s.executor.WithTx(ctx, databaseID, true, func(ctx context.Context, tx Tx) error {
		exec := txQueryExecutor{tx}
		position, batchID, err := peek(ctx, exec, databaseID, domain.UndoStackTable)
		if err != nil {
			return err
		}
		entries, err := s.fetchStackBatch(ctx, exec, databaseID, batchID, userID, refusal)
		if err != nil {
			return err
		}
		if err := s.checkReversible(ctx, exec, databaseID, entries); err != nil {
			return err
		}
		recordReversalSpan(ctx, databaseID, undoBatchID, entries)
		moves := []domain.SQLStatement{undoStackRemoveStmt(position), redoStackPushStmt(undoBatchID, userID)}
		outcome, err = s.applyReversalInTx(ctx, tx, databaseID, entries, userID, undoBatchID,
			domain.ChangeOriginUndo, moves)
		return err
	})
	if err != nil {
		return nil, err
	}
	result := s.finishReversal(ctx, databaseID, outcome)
	s.cleanupStack(ctx, databaseID, domain.RedoStackTable, userID)
	return result, nil
}

// redo re-applies the batch on top of the redo stack peek selects, inside one transaction. The undo's rows describe
// the inverse that ran, so reversing them like any other batch lands back at the original direction. The redo batch
// lands on the caller's undo stack and the rest of the caller's redo stack survives.
func (s *QueryService) redo(
	ctx context.Context, databaseID string, userID uuid.UUID, peek stackPeek, refusal string,
) (*domain.QueryResult, error) {
	redoBatchID := uuid.New().String()
	var outcome reversalOutcome
	err := s.executor.WithTx(ctx, databaseID, true, func(ctx context.Context, tx Tx) error {
		exec := txQueryExecutor{tx}
		position, undoBatchID, err := peek(ctx, exec, databaseID, domain.RedoStackTable)
		if err != nil {
			return err
		}
		entries, err := s.fetchStackBatch(ctx, exec, databaseID, undoBatchID, userID, refusal)
		if err != nil {
			return err
		}
		if err := s.checkReversible(ctx, exec, databaseID, entries); err != nil {
			return err
		}
		recordReversalSpan(ctx, databaseID, redoBatchID, entries)
		moves := []domain.SQLStatement{redoStackRemoveStmt(position), undoStackPushStmt(redoBatchID, userID)}
		outcome, err = s.applyReversalInTx(ctx, tx, databaseID, entries, userID, redoBatchID,
			domain.ChangeOriginRedo, moves)
		return err
	})
	if err != nil {
		return nil, err
	}
	result := s.finishReversal(ctx, databaseID, outcome)
	s.cleanupStack(ctx, databaseID, domain.UndoStackTable, userID)
	return result, nil
}

// fetchStackBatch loads the batch a stack entry points at, logging refusal when the batch has no rows left: retention
// pruned it, so the stack entry is stale.
func (s *QueryService) fetchStackBatch(
	ctx context.Context, exec internalExecutor, databaseID, batchID string, userID uuid.UUID, refusal string,
) ([]*domain.ChangeLogEntry, error) {
	entries, err := s.fetchChangeLogBatch(ctx, exec, databaseID, batchID)
	if errors.Is(err, domain.ErrChangeLogEntryNotFound) {
		s.dbSvc.logger.Warn().
			Str("database_id", databaseID).
			Str("source_batch_id", batchID).
			Str("user.id", userID.String()).
			Msg(refusal)
	}
	return entries, err
}

// checkReversible runs conflict detection over entries and returns the *ConflictError to refuse the reversal with,
// or nil when every entry can be reversed.
func (s *QueryService) checkReversible(
	ctx context.Context, exec internalExecutor, databaseID string, entries []*domain.ChangeLogEntry,
) error {
	conflict, err := s.detectChangeConflicts(ctx, exec, databaseID, entries)
	if err != nil {
		return err
	}
	return newConflictError(conflict)
}

// recordReversalSpan annotates the current span with the batch a reversal writes and the shape of what it reverses.
func recordReversalSpan(ctx context.Context, databaseID, batchID string, entries []*domain.ChangeLogEntry) {
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String("database_id", databaseID),
		attribute.String("batch_id", batchID),
		attribute.Int("entry_count", len(entries)),
		attribute.String("kind", string(entries[0].Kind)),
	)
}

// finishReversal runs the work that follows a committed reversal: the retention prune (the reversal wrote rows of
// its own), the stored-size refresh, and the affected-row sample of the batch the reversal wrote.
func (s *QueryService) finishReversal(ctx context.Context, databaseID string, o reversalOutcome) *domain.QueryResult {
	s.cleanupChangeLog(ctx, databaseID)
	s.dbSvc.RefreshStoredSize(ctx, databaseID)
	result := &domain.QueryResult{BatchID: o.batchID, AffectedRowCount: int64(o.rows)}
	result.AffectedRows, result.AffectedRowsTruncated = s.readAffectedRows(ctx, databaseID, o.batchID)
	return result
}

// peekStack returns the position and batch_id of the top entry pushed by
// userID in the given stack table. Returns ErrNothingToUndo or
// ErrNothingToRedo if the user has no entries.
func (s *QueryService) peekStack(
	ctx context.Context, exec internalExecutor, databaseID, stackTable string, userID uuid.UUID,
) (int64, string, error) {
	fetchSQL := fmt.Sprintf(
		"SELECT position, batch_id FROM %s WHERE user_id = ? ORDER BY position DESC LIMIT 1",
		stackTable,
	)
	result, err := exec.ExecuteInternal(ctx, databaseID, fetchSQL,
		[]domain.TypedValue{domain.TextValue(userID.String())})
	if err != nil {
		return 0, "", err
	}
	if len(result.Rows) == 0 {
		if stackTable == domain.UndoStackTable {
			return 0, "", domain.ErrNothingToUndo
		}
		return 0, "", domain.ErrNothingToRedo
	}

	row := result.Rows[0]
	var position int64
	if v, ok := row[0].Value.(int64); ok {
		position = v
	}
	batchID := row[1].String()

	return position, batchID, nil
}

// peekStackAnyone returns the position and batch_id of the top entry in the given stack table regardless of who pushed
// it. Returns ErrNothingToUndo / ErrNothingToRedo when the stack is empty. Used by the owner-only *Anyone variants.
func (s *QueryService) peekStackAnyone(
	ctx context.Context, exec internalExecutor, databaseID, stackTable string,
) (int64, string, error) {
	fetchSQL := fmt.Sprintf(
		"SELECT position, batch_id FROM %s ORDER BY position DESC LIMIT 1",
		stackTable,
	)
	result, err := exec.ExecuteInternal(ctx, databaseID, fetchSQL, nil)
	if err != nil {
		return 0, "", err
	}
	if len(result.Rows) == 0 {
		if stackTable == domain.UndoStackTable {
			return 0, "", domain.ErrNothingToUndo
		}
		return 0, "", domain.ErrNothingToRedo
	}

	row := result.Rows[0]
	var position int64
	if v, ok := row[0].Value.(int64); ok {
		position = v
	}
	return position, row[1].String(), nil
}

// fetchChangeLogBatch fetches all _change_log entries for a batch, newest first. A batch is all-DML or all-DDL
// except when a DDL statement's foreign-key actions or user triggers wrote rows: those DML entries precede the DDL
// entry that caused them.
func (s *QueryService) fetchChangeLogBatch(
	ctx context.Context, exec internalExecutor, databaseID, batchID string,
) ([]*domain.ChangeLogEntry, error) {
	fetchSQL := fmt.Sprintf("SELECT %s FROM %s WHERE batch_id = ? ORDER BY id DESC",
		changeLogFullColumns, domain.ChangeLogTable)
	result, err := exec.ExecuteInternal(ctx, databaseID, fetchSQL, []domain.TypedValue{domain.TextValue(batchID)})
	if err != nil {
		return nil, err
	}
	if len(result.Rows) == 0 {
		return nil, domain.ErrChangeLogEntryNotFound
	}
	entries := make([]*domain.ChangeLogEntry, len(result.Rows))
	for i, row := range result.Rows {
		entries[i] = parseChangeLogFullRow(row)
	}
	return entries, nil
}

// fetchChangeLogEntry retrieves a single _change_log entry by id.
func (s *QueryService) fetchChangeLogEntry(
	ctx context.Context, exec internalExecutor, databaseID string, changeLogID int64,
) (*domain.ChangeLogEntry, error) {
	fetchSQL := fmt.Sprintf("SELECT %s FROM %s WHERE id = ?", changeLogFullColumns, domain.ChangeLogTable)
	result, err := exec.ExecuteInternal(ctx, databaseID, fetchSQL,
		[]domain.TypedValue{domain.IntegerValue(changeLogID)})
	if err != nil {
		return nil, err
	}
	if len(result.Rows) == 0 {
		return nil, domain.ErrChangeLogEntryNotFound
	}
	return parseChangeLogFullRow(result.Rows[0]), nil
}

// decodeJSON decodes a JSON column into dst, keeping numbers as json.Number so that integers beyond 2^53 and the
// integer/real distinction survive into map[string]any values. Typed struct targets are unaffected.
func decodeJSON(blob string, dst any) error {
	dec := json.NewDecoder(strings.NewReader(blob))
	dec.UseNumber()
	return dec.Decode(dst)
}

// changeLogFullColumns is the SELECT list shared by every _change_log reader that decodes rows through
// parseChangeLogFullRow, which indexes columns by this order.
const changeLogFullColumns = "id, batch_id, kind, operation, origin, changed_by, changed_at, " +
	"table_name, row_id, old_values, new_values, " +
	"object_type, object_name, ddl_subtype, ddl_statement, ddl_metadata"

// parseChangeLogFullRow decodes a _change_log row selected with changeLogFullColumns into an entry carrying both
// the DML- and DDL-shaped fields.
//
// The JSON columns are written by the change-log triggers and the DDL pipeline, never by users, so a blob that
// fails to decode means a corrupted platform table. The failure is tolerated per field, leaving that field empty,
// rather than failing the whole read.
func parseChangeLogFullRow(row []domain.TypedValue) *domain.ChangeLogEntry {
	entry := &domain.ChangeLogEntry{
		BatchID:      row[1].String(),
		Kind:         domain.ChangeKind(row[2].String()),
		Operation:    row[3].String(),
		Origin:       row[4].String(),
		ChangedBy:    row[5].String(),
		ChangedAt:    domain.ParseSQLiteTimestamp(row[6].String()),
		TableName:    row[7].String(),
		RowID:        row[8].String(),
		ObjectType:   row[11].String(),
		ObjectName:   row[12].String(),
		DDLSubtype:   row[13].String(),
		DDLStatement: row[14].String(),
	}
	if id, ok := row[0].Value.(int64); ok {
		entry.ID = id
	}
	if oldJSON := row[9].String(); oldJSON != "" {
		_ = decodeJSON(oldJSON, &entry.OldValues)
	}
	if newJSON := row[10].String(); newJSON != "" {
		_ = decodeJSON(newJSON, &entry.NewValues)
	}
	if metaJSON := row[15].String(); metaJSON != "" {
		var m domain.DDLAlterMetadata
		if err := decodeJSON(metaJSON, &m); err == nil {
			entry.DDLMetadata = &m
		}
	}
	return entry
}

// fetchChangeSnapshot retrieves the optional _change_snapshots sidecar
// row for a _change_log entry. Returns (nil, nil) when the entry has no
// snapshot (non-destructive DDL or DML).
func (s *QueryService) fetchChangeSnapshot(
	ctx context.Context, exec internalExecutor, databaseID string, changeLogID int64,
) (*domain.ChangeSnapshot, error) {
	fetchSQL := fmt.Sprintf(
		"SELECT column_metadata, dependent_objects, row_payload, row_count_total, truncated "+
			"FROM %s WHERE change_log_id = ?",
		domain.ChangeSnapshotsTable,
	)
	result, err := exec.ExecuteInternal(
		ctx, databaseID, fetchSQL, []domain.TypedValue{domain.IntegerValue(changeLogID)},
	)
	if err != nil {
		return nil, err
	}
	if len(result.Rows) == 0 {
		return nil, nil
	}
	row := result.Rows[0]
	snap := &domain.ChangeSnapshot{ChangeLogID: changeLogID}
	if blob := row[0].String(); blob != "" {
		var meta domain.ColumnSnapshot
		if err := decodeJSON(blob, &meta); err == nil {
			snap.ColumnMetadata = &meta
		}
	}
	if blob := row[1].String(); blob != "" {
		_ = decodeJSON(blob, &snap.DependentObjects)
	}
	if blob := row[2].String(); blob != "" {
		_ = decodeJSON(blob, &snap.RowPayload)
	}
	if total, ok := row[3].Value.(int64); ok {
		snap.RowCountTotal = int(total)
	}
	if trunc, ok := row[4].Value.(int64); ok {
		snap.Truncated = trunc != 0
	}
	return snap, nil
}

// applyReversalInTx applies the inverse of a batch inside the open reversal transaction and records it under
// targetBatchID with origin, then closes the batch with stackStmts. Entries arrive newest first and are reversed run
// by run of kind, so a DROP TABLE whose foreign-key actions deleted child rows comes back before the rows do. A DML
// run runs its inverse SQL, which the tables' triggers record; a DDL run becomes one unit per entry and runs through
// the DDL pipeline, so the reversal is recorded exactly like user DDL: the inverse statement, its own pre-image
// snapshot, and the shape a later reversal inverts in turn.
func (s *QueryService) applyReversalInTx(
	ctx context.Context,
	tx Tx,
	databaseID string,
	entries []*domain.ChangeLogEntry,
	userID uuid.UUID,
	targetBatchID string,
	origin string,
	stackStmts []domain.SQLStatement,
) (reversalOutcome, error) {
	outcome := reversalOutcome{batchID: targetBatchID}
	for run := range domain.KindRuns(entries) {
		if run[0].Kind.IsDML() {
			if err := runDMLInverseInTx(ctx, tx, run); err != nil {
				return outcome, err
			}
			outcome.rows += len(run)
			continue
		}
		units, err := s.reversalUnits(ctx, tx, databaseID, run)
		if err != nil {
			return outcome, err
		}
		if _, _, err := s.runDDLUnitsInTx(ctx, tx, databaseID, userID, units, targetBatchID, origin); err != nil {
			return outcome, err
		}
	}
	return outcome, closeChangeBatchInTx(ctx, tx, userID, targetBatchID, origin, stackStmts)
}

// runDMLInverseInTx runs the inverse SQL of a run of DML entries inside tx; the tables' change-log triggers record
// the rows it writes.
func runDMLInverseInTx(ctx context.Context, tx Tx, entries []*domain.ChangeLogEntry) error {
	stmts, err := domain.GenerateBatchRevertSQL(entries)
	if err != nil {
		return fmt.Errorf("%w: %w", domain.ErrQueryExecution, err)
	}
	for _, st := range stmts {
		if _, err := tx.Exec(ctx, st.SQL, st.Args); err != nil {
			return fmt.Errorf("%w: %w", domain.ErrQueryExecution, explainExecError(err))
		}
	}
	return nil
}

// explainExecError swaps an engine error the platform has an explanation for — a BLOB refused by the change-log
// triggers rather than by the user's SQL, a bdb at its size cap — for the sentinel that states it, and returns any
// other error unchanged.
func explainExecError(err error) error {
	switch {
	case errors.Is(err, domain.ErrBlobUnsupported):
		return domain.ErrBlobUnsupported
	case errors.Is(err, domain.ErrDatabaseFull):
		return domain.ErrDatabaseFull
	}
	return err
}

// reversalUnits builds the unit that reverses each DDL entry, in the order given, loading the pre-image snapshot for
// the entries whose inverse needs one. A missing snapshot or an entry the generator rejects surfaces as a query
// execution error: the batch is not reversible as recorded.
func (s *QueryService) reversalUnits(
	ctx context.Context, tx Tx, databaseID string, entries []*domain.ChangeLogEntry,
) ([]domain.DDLUnit, error) {
	units := make([]domain.DDLUnit, 0, len(entries))
	for _, entry := range entries {
		var snap *domain.ChangeSnapshot
		if requiresSnapshotForRevert(entry) {
			loaded, err := s.fetchChangeSnapshot(ctx, txQueryExecutor{tx}, databaseID, entry.ID)
			if err != nil {
				return nil, err
			}
			if loaded == nil {
				s.dbSvc.logger.Warn().
					Str("database_id", databaseID).
					Int64("change_log_id", entry.ID).
					Str("object_type", entry.ObjectType).
					Str("object_name", entry.ObjectName).
					Str("ddl_subtype", entry.DDLSubtype).
					Msg("Cannot reverse destructive DDL: _change_snapshots row missing")
				return nil, fmt.Errorf("%w: missing snapshot for change_log id %d", domain.ErrQueryExecution, entry.ID)
			}
			snap = loaded
		}
		unit, err := domain.GenerateRevertDDL(entry, snap)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", domain.ErrQueryExecution, err)
		}
		units = append(units, unit)
	}
	return units, nil
}

// requiresSnapshotForRevert reports whether reverting the given entry
// needs the _change_snapshots sidecar (DROP * and DROP COLUMN are
// destructive — their inverse needs the pre-image data).
func requiresSnapshotForRevert(entry *domain.ChangeLogEntry) bool {
	if entry.Operation == domain.ChangeOpDrop {
		return true
	}
	if entry.Operation == domain.ChangeOpAlter && entry.DDLSubtype == domain.DDLSubtypeDropColumn {
		return true
	}
	return false
}

// executeDMLWithChangeLog runs N>=1 DML statements in a single atomic
// transaction:
//  1. Each user statement runs in order via tx.Exec with the authorizer
//     active, so a statement whose meaning changed since classification
//     (a trigger created in between) is still refused — per-table
//     _change_log triggers insert __pending__ rows for every row
//     touched.
//  2. After each statement, the cumulative __pending__ row count is
//     compared to maxRows; crossing the cap returns ErrTooManyRows and
//     rolls the whole transaction back.
//  3. One correction stamps changed_by, batch_id, and origin on every
//     __pending__ row (all stmts share one batch_id → one undo entry).
//  4. The batch is pushed to the undo stack; the calling user's redo
//     entries are cleared.
//
// After commit, affected rows are read back from _change_log filtered
// to kind='DML'. maxRows <= 0 disables the row-count cap.
func (s *QueryService) executeDMLWithChangeLog(
	ctx context.Context,
	databaseID string,
	userID uuid.UUID,
	origin string,
	stmts []domain.SQLStatement,
	maxRows int,
) (*domain.QueryResult, error) {
	batchID := uuid.New().String()

	correction := changeLogCorrectionStmt(userID, batchID, origin)
	pendingCountSQL := fmt.Sprintf(
		"SELECT COUNT(*) FROM %s WHERE batch_id = '%s' AND kind = '%s'",
		domain.ChangeLogTable, domain.ChangePendingBatchID, domain.ChangeKindDML,
	)

	var (
		totalAffected int64
		lastInsertID  *int64
		returned      *domain.QueryResult
	)
	err := s.executor.WithTx(ctx, databaseID, false, func(ctx context.Context, tx Tx) error {
		for i, stmt := range stmts {
			r, err := tx.Exec(ctx, stmt.SQL, stmt.Args)
			if err != nil && len(stmts) > 1 {
				return fmt.Errorf("statement %d: %w", i+1, explainExecError(err))
			}
			if err != nil {
				return explainExecError(err)
			}
			if r != nil {
				totalAffected += r.AffectedRowCount
				if r.LastInsertRowID != nil {
					lastInsertID = r.LastInsertRowID
				}
				// One result set per response: a second RETURNING would have to be dropped, so the request is
				// refused (and rolled back) instead.
				if len(r.Columns) > 0 {
					if returned != nil {
						return fmt.Errorf("%w: RETURNING is supported on one statement per request; statement %d "+
							"is the second", domain.ErrUnsupportedStatement, i+1)
					}
					returned = r
				}
			}
			if maxRows > 0 {
				cumulative, cerr := readPendingChangeLogCount(ctx, tx, pendingCountSQL)
				if cerr != nil {
					return fmt.Errorf("row-count check after statement %d: %w", i+1, cerr)
				}
				if cumulative > int64(maxRows) {
					return fmt.Errorf("%w: exceeded max rows (%d) at statement %d", domain.ErrTooManyRows, maxRows, i+1)
				}
			}
		}
		if _, err := tx.ExecInternal(ctx, correction.SQL, correction.Args); err != nil {
			return fmt.Errorf("change log correction: %w", err)
		}
		for _, push := range undoStackPushAndRedoClearStmts(batchID, userID) {
			if _, err := tx.ExecInternal(ctx, push.SQL, push.Args); err != nil {
				return fmt.Errorf("undo/redo stack update: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// The transaction has committed: the size refresh and the affected-rows read must not be dropped by a request
	// budget that fires right after it (each call is still capped by the executor's own ceiling).
	postCommitCtx := context.WithoutCancel(ctx)
	s.dbSvc.RefreshStoredSize(postCommitCtx, databaseID)

	result := &domain.QueryResult{
		BatchID:          batchID,
		AffectedRowCount: totalAffected,
		LastInsertRowID:  lastInsertID,
	}
	if returned != nil {
		result.Columns, result.Rows = returned.Columns, returned.Rows
	}
	result.AffectedRows, result.AffectedRowsTruncated = s.readAffectedRows(postCommitCtx, databaseID, batchID)
	return result, nil
}

// readPendingChangeLogCount returns the current count of __pending__ DML
// rows on tx — used by the per-request row-count cap to abort the
// transaction once the user's statements have produced more rows than
// allowed. Counting the table directly (vs summing per-statement
// AffectedRowCount) handles user-defined trigger cascades safely. The
// idx_change_log_batch_id index keeps cost bounded to the small
// __pending__ set the current tx is accumulating.
func readPendingChangeLogCount(ctx context.Context, tx Tx, countSQL string) (int64, error) {
	r, err := tx.Exec(ctx, countSQL, nil)
	if err != nil {
		return 0, err
	}
	if r == nil || len(r.Rows) == 0 || len(r.Rows[0]) == 0 {
		return 0, nil
	}
	if v, ok := r.Rows[0][0].Value.(int64); ok {
		return v, nil
	}
	return 0, nil
}

// readAffectedRows fetches DML _change_log entries for a batch, capped
// at affectedRowsMax. Returns the affected rows and whether the result
// was truncated. Best-effort: returns empty on error.
func (s *QueryService) readAffectedRows(ctx context.Context, databaseID, batchID string) ([]domain.AffectedRow, bool) {
	fetchSQL := fmt.Sprintf(
		"SELECT table_name, row_id, operation, old_values, new_values FROM %s "+
			"WHERE batch_id = ? AND kind = '%s' LIMIT %d",
		domain.ChangeLogTable, domain.ChangeKindDML, s.affectedRowsMax+1,
	)

	result, err := s.executor.Execute(ctx, databaseID, fetchSQL, []domain.TypedValue{domain.TextValue(batchID)})
	if err != nil {
		s.dbSvc.logger.Warn().Err(err).Str("batch_id", batchID).Msg("failed to read affected rows")
		return nil, false
	}

	truncated := len(result.Rows) > s.affectedRowsMax
	rows := result.Rows
	if truncated {
		rows = rows[:s.affectedRowsMax]
	}

	affected := make([]domain.AffectedRow, len(rows))
	for i, row := range rows {
		ar := domain.AffectedRow{
			TableName: row[0].String(),
			RowID:     row[1].String(),
			Operation: row[2].String(),
		}
		if oldJSON := row[3].String(); oldJSON != "" {
			_ = decodeJSON(oldJSON, &ar.OldValues)
		}
		if newJSON := row[4].String(); newJSON != "" {
			_ = decodeJSON(newJSON, &ar.NewValues)
		}
		affected[i] = ar
	}

	return affected, truncated
}

// --- Stack statement helpers ---
// These return domain.SQLStatement values for use in transactions.

func undoStackPushStmt(batchID string, userID uuid.UUID) domain.SQLStatement {
	return domain.SQLStatement{
		SQL: fmt.Sprintf("INSERT INTO %s (batch_id, user_id) VALUES (?, ?)", domain.UndoStackTable),
		Args: []domain.TypedValue{
			domain.TextValue(batchID),
			domain.TextValue(userID.String()),
		},
	}
}

// redoStackClearStmt clears the calling user's redo entries only. A new
// mutation by userX invalidates only userX's redo timeline; other users'
// redos persist (the conflict detector catches cross-user interference
// at redo time). DDL no longer clears everyone's stacks — it follows the
// same per-user redo invalidation rule.
func redoStackClearStmt(userID uuid.UUID) domain.SQLStatement {
	return domain.SQLStatement{
		SQL:  fmt.Sprintf("DELETE FROM %s WHERE user_id = ?", domain.RedoStackTable),
		Args: []domain.TypedValue{domain.TextValue(userID.String())},
	}
}

func undoStackRemoveStmt(position int64) domain.SQLStatement {
	return domain.SQLStatement{
		SQL:  fmt.Sprintf("DELETE FROM %s WHERE position = ?", domain.UndoStackTable),
		Args: []domain.TypedValue{domain.IntegerValue(position)},
	}
}

func redoStackRemoveStmt(position int64) domain.SQLStatement {
	return domain.SQLStatement{
		SQL:  fmt.Sprintf("DELETE FROM %s WHERE position = ?", domain.RedoStackTable),
		Args: []domain.TypedValue{domain.IntegerValue(position)},
	}
}

func redoStackPushStmt(batchID string, userID uuid.UUID) domain.SQLStatement {
	return domain.SQLStatement{
		SQL: fmt.Sprintf("INSERT INTO %s (batch_id, user_id) VALUES (?, ?)", domain.RedoStackTable),
		Args: []domain.TypedValue{
			domain.TextValue(batchID),
			domain.TextValue(userID.String()),
		},
	}
}

// undoStackPushAndRedoClearStmts returns the two statements that close out a
// new user mutation: push batchID onto the undo stack tagged with userID,
// then clear the calling user's redo entries. Other users' redo timelines
// are unaffected — the conflict detector handles cross-user interference
// at redo time.
func undoStackPushAndRedoClearStmts(batchID string, userID uuid.UUID) []domain.SQLStatement {
	return []domain.SQLStatement{undoStackPushStmt(batchID, userID), redoStackClearStmt(userID)}
}

// changeLogCorrectionStmt returns the UPDATE that stamps changed_by,
// batch_id, and origin on every __pending__ DML row the prior
// statements' triggers wrote. Issued at the end of any DML transaction
// (user write, undo, redo, revert) so the trigger-fired rows get the
// real metadata for the request.
func changeLogCorrectionStmt(userID uuid.UUID, batchID, origin string) domain.SQLStatement {
	sql := fmt.Sprintf(
		"UPDATE %s SET changed_by = ?, batch_id = ?, origin = ? "+
			"WHERE batch_id = '%s' AND kind = '%s'",
		domain.ChangeLogTable, domain.ChangePendingBatchID, domain.ChangeKindDML,
	)
	return domain.SQLStatement{
		SQL: sql,
		Args: []domain.TypedValue{
			domain.TextValue(userID.String()),
			domain.TextValue(batchID),
			domain.TextValue(origin),
		},
	}
}

// cleanupStack caps the given stack at the role-resolved per-user max
// (via quotaResolver.UndoStackMaxSizeForRoles, with the same cap applied
// to the redo stack) and prunes entries pointing at mutation log batches
// that have been deleted by retention. Best-effort: errors are logged,
// not returned.
//
// Per-user cap: each user gets their own budget — one prolific user
// cannot evict another's entries. Roles are pulled from ctx via
// authz.RolesFromContext so downstream distributions implementing
// QuotaResolver can raise limits per role without further service plumbing.
//
// Global orphan prune: orphans are user-agnostic (a deleted mutation
// log batch is gone for everyone), so the prune runs without a
// user_id filter.
func (s *QueryService) cleanupStack(ctx context.Context, databaseID, stackTable string, userID uuid.UUID) {
	roles := authz.RolesFromContext(ctx)
	maxSize := s.quotaResolver(roles).UndoStackMaxSize
	if maxSize <= 0 {
		maxSize = domain.DefaultUndoStackMaxSize
	}

	capSQL := fmt.Sprintf(
		"DELETE FROM %s WHERE user_id = ? AND position NOT IN ("+
			"SELECT position FROM %s WHERE user_id = ? ORDER BY position DESC LIMIT ?)",
		stackTable, stackTable,
	)
	if _, err := s.executor.ExecuteInternal(ctx, databaseID, capSQL, []domain.TypedValue{
		domain.TextValue(userID.String()),
		domain.TextValue(userID.String()),
		domain.IntegerValue(int64(maxSize)),
	}); err != nil {
		s.dbSvc.logger.Warn().Err(err).Str("stack", stackTable).Msg("failed to cap stack")
	}

	pruneSQL := fmt.Sprintf(
		"DELETE FROM %s WHERE batch_id NOT IN (SELECT DISTINCT batch_id FROM %s)",
		stackTable, domain.ChangeLogTable,
	)
	if _, err := s.executor.ExecuteInternal(ctx, databaseID, pruneSQL, nil); err != nil {
		s.dbSvc.logger.Warn().Err(err).Str("stack", stackTable).Msg("failed to prune stack")
	}
}

// cleanupChangeLog deletes _change_log entries older than the retention period. _change_snapshots rows for the
// pruned entries are cascade-deleted by the FK ON DELETE CASCADE — no separate cleanup needed.
// Best-effort: failures are logged but do not affect the user's request.
func (s *QueryService) cleanupChangeLog(ctx context.Context, databaseID string) {
	cleanupSQL := fmt.Sprintf(
		"DELETE FROM %s WHERE changed_at < strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', 'now', '-%d days')",
		domain.ChangeLogTable, domain.ChangeLogRetentionDays,
	)

	if _, err := s.executor.ExecuteInternal(ctx, databaseID, cleanupSQL, nil); err != nil {
		s.dbSvc.logger.Warn().Err(err).Msg("failed to clean up old _change_log entries")
	}
}

// pragmaResultToTriggerColumns converts a PRAGMA table_info result to TriggerColumn slice.
// PRAGMA table_info returns rows with columns: cid, name, type, notnull, dflt_value, pk.
func pragmaResultToTriggerColumns(result *domain.QueryResult) []domain.TriggerColumn {
	if result == nil || len(result.Rows) == 0 {
		return nil
	}

	columns := make([]domain.TriggerColumn, 0, len(result.Rows))
	for _, row := range result.Rows {
		// Column name is at index 1 in PRAGMA table_info output.
		if len(row) < 2 {
			continue
		}
		name, ok := row[1].Value.(string)
		if !ok || name == "" {
			continue
		}
		columns = append(columns, domain.TriggerColumn{Name: name})
	}

	return columns
}

// validateDDLAdmission runs the per-statement admission checks on one classified DDL statement: every
// created object name (table, index, trigger, view, or virtual table) must be a valid user identifier,
// and WITHOUT ROWID tables are rejected.
func validateDDLAdmission(cs domain.ClassifiedStatement) error {
	for _, name := range cs.CreatedObjects {
		if err := domain.ValidateIdentifier(name); err != nil {
			return err
		}
	}
	return validateWithoutRowID(cs.SQL)
}

// validateWithoutRowID rejects CREATE TABLE statements that use WITHOUT ROWID.
func validateWithoutRowID(sql string) error {
	if strings.Contains(strings.ToUpper(sql), "WITHOUT ROWID") {
		return fmt.Errorf("%w: mutation log triggers require rowid", domain.ErrWithoutRowIDNotSupported)
	}
	return nil
}

// requiredPermission maps a statement type to the database permission needed to execute it.
func requiredPermission(stmtType domain.StatementType) (authz.DatabasePermission, error) {
	switch stmtType {
	case domain.StatementDQL:
		return authz.DBPermRead, nil
	case domain.StatementDML:
		return authz.DBPermWrite, nil
	case domain.StatementDDL:
		return authz.DBPermManageSchema, nil
	default:
		return "", fmt.Errorf("%w: cannot determine permission", domain.ErrUnsupportedStatement)
	}
}
