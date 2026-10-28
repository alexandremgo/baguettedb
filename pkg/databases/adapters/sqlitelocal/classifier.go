package sqlitelocal

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// Classifier classifies SQL statements via SQLite's native parser. Stateless —
// the schema lives on the service.ParseHandle passed to each method, which
// runs against the user's bdb database.
type Classifier struct{}

// NewClassifier returns a Classifier.
func NewClassifier() *Classifier { return &Classifier{} }

// translatePrepareError maps SQLite prepare-time errors into domain
// errors callers can pattern-match on. Authorizer denials are
// disambiguated via the reason the authorizer recorded — reserved-prefix
// becomes ErrReservedTable; ATTACH/DETACH, disallowed PRAGMAs, temporary
// objects and REINDEX become ErrUnsupportedStatement. Non-auth errors
// pass through.
func translatePrepareError(err error, reason domain.DenyReason) error {
	if err == nil {
		return nil
	}
	if !IsCode(err, CodeAuth) {
		return err
	}
	switch reason {
	case domain.DenyReservedPrefix:
		return fmt.Errorf("%w: %w", domain.ErrReservedTable, err)
	case domain.DenyTransactionControl:
		return fmt.Errorf("%w: "+
			"transaction control statements (BEGIN/COMMIT/ROLLBACK/SAVEPOINT/RELEASE) are managed by the platform",
			domain.ErrUnsupportedStatement)
	case domain.DenyTempObject:
		return fmt.Errorf("%w: temporary objects are not supported — use a CTE (WITH ...) for intermediate results, "+
			"or a regular table for scratch data", domain.ErrUnsupportedStatement)
	case domain.DenyReindex:
		return fmt.Errorf("%w: REINDEX is managed by the platform", domain.ErrUnsupportedStatement)
	default:
		return fmt.Errorf("%w: %w", domain.ErrUnsupportedStatement, err)
	}
}

// Classify walks all statements in sql and returns one
// ClassifiedStatement per parsed statement, in order. Trailing
// whitespace and comments are tolerated. Returns ErrUnsupportedStatement
// if the input contains no statements (empty / whitespace / comments
// only). SQL that SQLite cannot prepare (a syntax error, an unknown
// table or column) returns an *InvalidSQLError carrying SQLite's message;
// an authorizer denial is prefixed with the 1-indexed statement number
// that failed.
func (c *Classifier) Classify(handle service.ParseHandle, sql string) ([]domain.ClassifiedStatement, error) {
	var out []domain.ClassifiedStatement
	remaining := sql
	for stmtIdx := 1; ; stmtIdx++ {
		if strings.TrimSpace(remaining) == "" {
			break
		}
		res, err := handle.Parse(remaining)
		if sqlErr, ok := errors.AsType[*Error](err); ok && !IsCode(err, CodeAuth) {
			invalid := &domain.InvalidSQLError{Message: sqlErr.Msg}
			if stmtIdx > 1 {
				invalid.Statement = stmtIdx
			}
			return nil, invalid
		}
		if err != nil {
			return nil, fmt.Errorf("statement %d: %w", stmtIdx, translatePrepareError(err, res.DenyReason))
		}
		// Pre-statement bytes consumed; the remainder lives in res.Tail.
		// SQLite's pzTail is a pointer into the input buffer, so
		// len(remaining) - len(res.Tail) is the number of bytes the
		// parser consumed for this iteration (including any leading
		// whitespace/comments before the statement).
		consumed := len(remaining) - len(res.Tail)
		if consumed <= 0 {
			// Defensive: prepare reported no progress. Stop to avoid
			// looping; the trailing remainder is treated as empty.
			break
		}
		stmtSQL := remaining[:consumed]
		remaining = res.Tail

		if res.Empty {
			// Whitespace/comments-only chunk before the next real
			// statement (or trailing). Skip without recording.
			continue
		}

		// Positive identification only — anything that isn't DQL, DDL,
		// or DML is rejected as unsupported.
		var stype domain.StatementType
		switch {
		case res.IsReadOnly:
			stype = domain.StatementDQL
		case res.DDLKind != domain.DDLUnknown:
			stype = domain.StatementDDL
		case res.IsDML:
			stype = domain.StatementDML
		default:
			return nil, fmt.Errorf("statement %d: %w: not a SELECT, DML, or DDL statement",
				stmtIdx, domain.ErrUnsupportedStatement)
		}

		out = append(out, domain.ClassifiedStatement{
			SQL:            stmtSQL,
			Type:           stype,
			DDLType:        res.DDLKind,
			Tables:         dedupePreserveOrder(res.Tables),
			CreatedObjects: res.CreatedObjects,
			DMLVerbs:       res.DMLVerbs,
			ResultColumns:  res.ResultColumns,
			ParamNames:     res.ParamNames,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: empty SQL", domain.ErrUnsupportedStatement)
	}
	return out, nil
}

// ClassifyAlterTable parses an ALTER TABLE statement and returns its
// subtype + relevant names. Errors if sql isn't an ALTER TABLE.
//
// SQLite's authorizer reports SQLITE_ALTER_TABLE with the table name but
// not the subtype (RENAME TO / RENAME COLUMN / ADD COLUMN / DROP
// COLUMN), so the subtype + new-column / new-table names are extracted
// via small regex matches over the SQL text.
func (c *Classifier) ClassifyAlterTable(handle service.ParseHandle, sql string) (domain.AlterTableInfo, error) {
	res, err := handle.Parse(sql)
	if err != nil {
		return domain.AlterTableInfo{}, translatePrepareError(err, res.DenyReason)
	}

	if strings.TrimSpace(res.Tail) != "" {
		return domain.AlterTableInfo{}, fmt.Errorf("%w: multi-statement queries are not allowed",
			domain.ErrUnsupportedStatement)
	}
	if res.DDLKind != domain.DDLAlterTable {
		return domain.AlterTableInfo{}, fmt.Errorf("%w: not an ALTER TABLE statement", domain.ErrUnsupportedStatement)
	}

	info, ok := parseAlterTable(sql)
	if !ok {
		return domain.AlterTableInfo{}, fmt.Errorf("%w: unrecognized ALTER TABLE subtype",
			domain.ErrUnsupportedStatement)
	}
	return info, nil
}

// dedupePreserveOrder returns a new slice with duplicates removed,
// keeping first-occurrence order.
func dedupePreserveOrder(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// ALTER TABLE subtype regexes. Identifiers may be bare, double-quoted, or
// backtick-quoted; the regex strips quotes via alternation. Keep these
// strict — we want unrecognized shapes to fall through to "unsupported"
// rather than silently misclassify.
var (
	altRenameTo = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+` + ident + `\s+RENAME\s+TO\s+` + ident +
		`\s*;?\s*$`)
	altRenameColumn = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+` + ident + `\s+RENAME\s+(?:COLUMN\s+)?` + ident +
		`\s+TO\s+` + ident + `\s*;?\s*$`)
	// The column ident must be followed by whitespace, ';', or end of input — a \b would fail after a
	// quoted ident's closing quote and let the regex backtrack into matching the COLUMN keyword itself.
	altAddColumn = regexp.MustCompile(
		`(?is)^\s*ALTER\s+TABLE\s+` + ident + `\s+ADD\s+(?:COLUMN\s+)?` + ident + `(?:[\s;]|$)`)
	altDropColumn = regexp.MustCompile(
		`(?is)^\s*ALTER\s+TABLE\s+` + ident + `\s+DROP\s+(?:COLUMN\s+)?` + ident + `\s*;?\s*$`)
)

// ident matches a SQL identifier — bare, double-quoted, or backtick-quoted.
const ident = `(?:` +
	`"([^"]+)"` + // double-quoted
	`|` + "`([^`]+)`" + // backtick-quoted
	`|([A-Za-z_][A-Za-z0-9_]*)` + // bare
	`)`

// extractIdent picks the non-empty capture group from an ident match.
// Each ident pattern produces 3 groups (quoted, backtick, bare); only
// one is non-empty.
func extractIdent(groups []string, base int) string {
	for i := range 3 {
		if groups[base+i] != "" {
			return groups[base+i]
		}
	}
	return ""
}

// parseAlterTable runs the ALTER subtype regexes and extracts the
// relevant identifiers. Returns ok=false if no shape matches.
func parseAlterTable(sql string) (domain.AlterTableInfo, bool) {
	// RENAME TO: 2 idents (table, new table) → 6 capture groups.
	if m := altRenameTo.FindStringSubmatch(sql); m != nil {
		return domain.AlterTableInfo{
			Subtype:      domain.AlterTableRenameTo,
			TableName:    extractIdent(m, 1),
			NewTableName: extractIdent(m, 4),
		}, true
	}
	// RENAME COLUMN: 3 idents (table, column, new column) → 9 capture groups.
	if m := altRenameColumn.FindStringSubmatch(sql); m != nil {
		return domain.AlterTableInfo{
			Subtype:       domain.AlterTableRenameColumn,
			TableName:     extractIdent(m, 1),
			ColumnName:    extractIdent(m, 4),
			NewColumnName: extractIdent(m, 7),
		}, true
	}
	// ADD COLUMN: 2 idents (table, column) — column may be followed by type/constraints.
	if m := altAddColumn.FindStringSubmatch(sql); m != nil {
		return domain.AlterTableInfo{
			Subtype:    domain.AlterTableAddColumn,
			TableName:  extractIdent(m, 1),
			ColumnName: extractIdent(m, 4),
		}, true
	}
	// DROP COLUMN: 2 idents (table, column).
	if m := altDropColumn.FindStringSubmatch(sql); m != nil {
		return domain.AlterTableInfo{
			Subtype:    domain.AlterTableDropColumn,
			TableName:  extractIdent(m, 1),
			ColumnName: extractIdent(m, 4),
		}, true
	}
	return domain.AlterTableInfo{}, false
}
