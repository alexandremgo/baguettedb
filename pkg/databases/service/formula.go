package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// formulaColumns is the ordered column list for _formulas reads and scans.
const formulaColumns = "id, name, description, sql, kind, params, " +
	"attached_to_formula_id, display_order, created_at, updated_at, statement_verb, display, hidden, label"

// FormulaService manages per-bdb formulas (named, parameterized SQL artifacts).
// Formulas live in the per-bdb SQLite platform table _formulas. It holds a QueryService
// so a formula run reuses the exact execute path raw SQL takes — a formula grants no capability beyond it.
type FormulaService struct {
	dbSvc      *DatabaseService
	executor   queryExecutor
	classifier statementClassifier
	querySvc   *QueryService
	runTimeout time.Duration
	logger     zerolog.Logger
}

// NewFormulaService creates a new FormulaService. runTimeout bounds a single formula run from the
// app; a non-positive value disables the per-run deadline (the general query ceiling still applies).
func NewFormulaService(
	dbSvc *DatabaseService, executor queryExecutor, classifier statementClassifier,
	querySvc *QueryService, runTimeout time.Duration, logger zerolog.Logger,
) *FormulaService {
	return &FormulaService{
		dbSvc:      dbSvc,
		executor:   executor,
		classifier: classifier,
		querySvc:   querySvc,
		runTimeout: runTimeout,
		logger:     logger,
	}
}

// FormulaInput is the payload for creating a new formula. AttachedToFormulaID is
// nil for a top-level formula. DisplayOrder is nil to append after existing siblings.
type FormulaInput struct {
	Name string
	// Label is the optional human name the app renders instead of Name; empty falls back to Name.
	Label               string
	Description         string
	SQL                 string
	Params              domain.ParamSchema
	AttachedToFormulaID *uuid.UUID
	DisplayOrder        *int
	// Display is a view's optional presentation config; slot refs may name attached formulas by
	// name or UUID and are normalized to UUIDs before storage.
	Display *domain.FormulaDisplay
	// Hidden marks the formula assistant-only: the app omits it from its default listing.
	Hidden bool
}

// FormulaPatch is the payload for partially updating a formula. A nil pointer
// means "leave unchanged"; a non-nil pointer means "replace with this value".
type FormulaPatch struct {
	Name *string
	// Label replaces the human label when non-nil; an empty string clears it, reverting to Name.
	Label        *string
	Description  *string
	SQL          *string
	Params       *domain.ParamSchema
	DisplayOrder *int
	// Display replaces the display config when non-nil; ClearDisplay removes it (reverting to the
	// default table rendering). Setting both is rejected.
	Display      *domain.FormulaDisplay
	ClearDisplay bool
	Hidden       *bool
}

// CreateFormula validates the input, derives the formula kind from its SQL, and
// inserts the formula. Returns ErrFormulaNameTaken if the name is already in use.
func (s *FormulaService) CreateFormula(
	ctx context.Context, userID, bdbID uuid.UUID, input FormulaInput,
) (*domain.Formula, error) {
	ctx, span := tracer.Start(ctx, "FormulaService.CreateFormula",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("formula.name", input.Name),
		),
	)
	defer span.End()

	if err := domain.ValidateFormulaName(input.Name); err != nil {
		return nil, err
	}
	if err := domain.ValidateFormulaLabel(input.Label); err != nil {
		return nil, err
	}
	if err := domain.ValidateFormulaDescription(input.Description); err != nil {
		return nil, err
	}
	if err := domain.ValidateFormulaSQL(input.SQL); err != nil {
		return nil, err
	}
	if err := input.Params.Validate(); err != nil {
		return nil, err
	}

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermWrite)
	if err != nil {
		return nil, err
	}
	databaseID := db.ID.String()

	kind, verb, paramNames, resultCols, err := s.classifyFormulaSQL(ctx, databaseID, input.SQL)
	if err != nil {
		return nil, err
	}
	if input.AttachedToFormulaID != nil {
		if err := s.verifyAttachTarget(ctx, databaseID, *input.AttachedToFormulaID, kind, input.Params); err != nil {
			return nil, err
		}
	}
	if err := domain.CrossCheckParams(input.Params, paramNames); err != nil {
		return nil, err
	}
	if err := validateBulkPlaceholder(input.Params, input.SQL); err != nil {
		return nil, err
	}
	if err := s.verifyLookupTargets(ctx, databaseID, input.Params); err != nil {
		return nil, err
	}

	display := input.Display
	if display != nil {
		if kind != domain.FormulaKindView {
			return nil, fmt.Errorf("%w: a display config is only valid on a view", domain.ErrFormulaDisplayInvalid)
		}
		if err := domain.ValidateFormulaDisplay(*display, resultCols, input.Params,
			input.AttachedToFormulaID != nil); err != nil {
			return nil, err
		}
		// A new view has no attached actions yet, so any filled slot fails resolution here: author
		// the view first, create its actions attached to it, then set the slots via update.
		normalized, err := resolveDisplaySlots(*display, nil, input.AttachedToFormulaID != nil)
		if err != nil {
			return nil, err
		}
		display = &normalized
	}

	order, err := s.resolveDisplayOrder(ctx, databaseID, input.DisplayOrder, input.AttachedToFormulaID)
	if err != nil {
		return nil, err
	}

	params := input.Params
	if params == nil {
		params = domain.ParamSchema{}
	}
	paramsJSON, err := marshalParams(params)
	if err != nil {
		return nil, fmt.Errorf("encoding formula params: %w", err)
	}

	displayArg, err := marshalDisplayArg(display)
	if err != nil {
		return nil, fmt.Errorf("encoding formula display: %w", err)
	}

	now := time.Now().UTC()
	formula := &domain.Formula{
		ID:                  uuid.New(),
		Name:                input.Name,
		Label:               input.Label,
		Description:         input.Description,
		SQL:                 input.SQL,
		Kind:                kind,
		StatementVerb:       verb,
		Params:              params,
		Display:             display,
		Hidden:              input.Hidden,
		AttachedToFormulaID: input.AttachedToFormulaID,
		DisplayOrder:        order,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	insertSQL := fmt.Sprintf(
		"INSERT INTO %s (id, name, label, description, sql, kind, statement_verb, params, "+
			"attached_to_formula_id, display_order, created_at, updated_at, display, hidden) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		domain.FormulasTable,
	)
	args := []domain.TypedValue{
		domain.TextValue(formula.ID.String()),
		domain.TextValue(formula.Name),
		domain.TextValue(formula.Label),
		domain.TextValue(formula.Description),
		domain.TextValue(formula.SQL),
		domain.TextValue(string(formula.Kind)),
		domain.TextValue(string(formula.StatementVerb)),
		domain.TextValue(paramsJSON),
		attachArg(formula.AttachedToFormulaID),
		domain.IntegerValue(int64(formula.DisplayOrder)),
		domain.TextValue(domain.FormatSQLiteTimestamp(formula.CreatedAt)),
		domain.TextValue(domain.FormatSQLiteTimestamp(formula.UpdatedAt)),
		displayArg,
		boolArg(formula.Hidden),
	}

	if _, err := s.executor.ExecuteInternal(ctx, databaseID, insertSQL, args); err != nil {
		if errors.Is(err, domain.ErrUniqueViolation) {
			return nil, fmt.Errorf("%w: %s", domain.ErrFormulaNameTaken, input.Name)
		}
		return nil, fmt.Errorf("creating formula: %w", err)
	}

	return formula, nil
}

// UpdateFormula applies a partial patch to an existing formula resolved by name or
// UUID. When the SQL changes, the kind is re-derived. Returns the reloaded formula.
func (s *FormulaService) UpdateFormula(
	ctx context.Context, userID, bdbID uuid.UUID, ref string, patch FormulaPatch,
) (*domain.Formula, error) {
	ctx, span := tracer.Start(ctx, "FormulaService.UpdateFormula",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("formula.ref", ref),
		),
	)
	defer span.End()

	if patch.Name != nil {
		if err := domain.ValidateFormulaName(*patch.Name); err != nil {
			return nil, err
		}
	}
	if patch.Label != nil {
		if err := domain.ValidateFormulaLabel(*patch.Label); err != nil {
			return nil, err
		}
	}
	if patch.Description != nil {
		if err := domain.ValidateFormulaDescription(*patch.Description); err != nil {
			return nil, err
		}
	}
	if patch.SQL != nil {
		if err := domain.ValidateFormulaSQL(*patch.SQL); err != nil {
			return nil, err
		}
	}
	if patch.Params != nil {
		if err := patch.Params.Validate(); err != nil {
			return nil, err
		}
	}

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermWrite)
	if err != nil {
		return nil, err
	}
	databaseID := db.ID.String()

	if patch.Display != nil && patch.ClearDisplay {
		return nil, fmt.Errorf("%w: cannot both set and clear the display config", domain.ErrFormulaDisplayInvalid)
	}

	existing, err := s.fetchFormulaByRef(ctx, databaseID, ref)
	if err != nil {
		return nil, err
	}

	// The effective values a guard must judge: the patched value where supplied, the stored one
	// otherwise. Result columns are captured lazily — only a display (re-)validation needs them.
	effectiveKind := existing.Kind
	effectiveVerb := existing.StatementVerb
	effectiveParams := existing.Params
	if patch.Params != nil {
		effectiveParams = *patch.Params
	}
	var resultCols []string
	haveResultCols := false

	setClauses := []string{"updated_at = ?"}
	args := []domain.TypedValue{domain.TextValue(domain.FormatSQLiteTimestamp(time.Now()))}
	if patch.Name != nil {
		setClauses = append(setClauses, "name = ?")
		args = append(args, domain.TextValue(*patch.Name))
	}
	if patch.Label != nil {
		setClauses = append(setClauses, "label = ?")
		args = append(args, domain.TextValue(*patch.Label))
	}
	if patch.Description != nil {
		setClauses = append(setClauses, "description = ?")
		args = append(args, domain.TextValue(*patch.Description))
	}
	// When the SQL or the params change, re-reconcile the effective combination (patched value
	// where supplied, otherwise the stored one) so the schema never drifts from the placeholders the SQL actually uses.
	if patch.SQL != nil {
		kind, verb, paramNames, cols, cerr := s.classifyFormulaSQL(ctx, databaseID, *patch.SQL)
		if cerr != nil {
			return nil, cerr
		}
		if err := s.verifyKindStable(ctx, databaseID, existing, kind); err != nil {
			return nil, err
		}
		if err := domain.CrossCheckParams(effectiveParams, paramNames); err != nil {
			return nil, err
		}
		if err := validateBulkPlaceholder(effectiveParams, *patch.SQL); err != nil {
			return nil, err
		}
		effectiveKind, effectiveVerb = kind, verb
		resultCols, haveResultCols = cols, true
		setClauses = append(setClauses, "sql = ?", "kind = ?", "statement_verb = ?")
		args = append(args, domain.TextValue(*patch.SQL), domain.TextValue(string(kind)),
			domain.TextValue(string(verb)))
	} else if patch.Params != nil {
		_, _, paramNames, cols, cerr := s.classifyFormulaSQL(ctx, databaseID, existing.SQL)
		if cerr != nil {
			return nil, cerr
		}
		if err := domain.CrossCheckParams(*patch.Params, paramNames); err != nil {
			return nil, err
		}
		if err := validateBulkPlaceholder(*patch.Params, existing.SQL); err != nil {
			return nil, err
		}
		resultCols, haveResultCols = cols, true
	}
	if patch.Params != nil {
		// An attached row-detail view must keep params a row click can bind — a params change may
		// not drop :rowid or introduce a required prompt (detaching via update is not possible).
		if existing.AttachedToFormulaID != nil && existing.Kind == domain.FormulaKindView {
			if err := domain.ValidateRowDetailParams(*patch.Params); err != nil {
				return nil, err
			}
		}
		if err := s.verifyLookupTargets(ctx, databaseID, *patch.Params); err != nil {
			return nil, err
		}
		paramsJSON, err := marshalParams(*patch.Params)
		if err != nil {
			return nil, fmt.Errorf("encoding formula params: %w", err)
		}
		setClauses = append(setClauses, "params = ?")
		args = append(args, domain.TextValue(paramsJSON))
	}
	if patch.DisplayOrder != nil {
		setClauses = append(setClauses, "display_order = ?")
		args = append(args, domain.IntegerValue(int64(*patch.DisplayOrder)))
	}

	// An attached action referenced by a slot of its parent view's display must keep the slot's
	// shape — a SQL or params change may not flip its verb or break its parameter rules.
	if existing.AttachedToFormulaID != nil && (patch.SQL != nil || patch.Params != nil) {
		if err := s.verifySlotShapeKept(ctx, databaseID, existing, effectiveKind, effectiveVerb,
			effectiveParams); err != nil {
			return nil, err
		}
	}

	effectiveDisplay := existing.Display
	switch {
	case patch.ClearDisplay:
		effectiveDisplay = nil
		setClauses = append(setClauses, "display = ?")
		args = append(args, domain.NullValue())
	case patch.Display != nil:
		effectiveDisplay = patch.Display
	}
	// A set or surviving display config is re-judged whenever it or the SQL changes: the mode
	// needs a view, the named columns must still come out of the SELECT, and slot refs must
	// resolve to attached actions of the right shape.
	if effectiveDisplay != nil && (patch.Display != nil || patch.SQL != nil) {
		if effectiveKind != domain.FormulaKindView {
			return nil, fmt.Errorf("%w: a display config is only valid on a view — clear it first",
				domain.ErrFormulaDisplayInvalid)
		}
		if !haveResultCols {
			_, _, _, cols, cerr := s.classifyFormulaSQL(ctx, databaseID, existing.SQL)
			if cerr != nil {
				return nil, cerr
			}
			resultCols = cols
		}
		viewAttached := existing.AttachedToFormulaID != nil
		if err := domain.ValidateFormulaDisplay(*effectiveDisplay, resultCols, effectiveParams,
			viewAttached); err != nil {
			return nil, err
		}
		attached, aerr := s.fetchAttachedFormulas(ctx, databaseID, existing.ID)
		if aerr != nil {
			return nil, aerr
		}
		normalized, rerr := resolveDisplaySlots(*effectiveDisplay, attached, viewAttached)
		if rerr != nil {
			return nil, rerr
		}
		if patch.Display != nil {
			displayArg, merr := marshalDisplayArg(&normalized)
			if merr != nil {
				return nil, fmt.Errorf("encoding formula display: %w", merr)
			}
			setClauses = append(setClauses, "display = ?")
			args = append(args, displayArg)
		}
	}

	if patch.Hidden != nil {
		setClauses = append(setClauses, "hidden = ?")
		args = append(args, boolArg(*patch.Hidden))
	}
	args = append(args, domain.TextValue(existing.ID.String()))

	updateSQL := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", domain.FormulasTable, strings.Join(setClauses, ", "))
	if _, err := s.executor.ExecuteInternal(ctx, databaseID, updateSQL, args); err != nil {
		if errors.Is(err, domain.ErrUniqueViolation) {
			return nil, fmt.Errorf("%w", domain.ErrFormulaNameTaken)
		}
		return nil, fmt.Errorf("updating formula: %w", err)
	}

	return s.fetchFormulaByRef(ctx, databaseID, existing.ID.String())
}

// GetFormula resolves a formula by name or UUID. Visible to viewer+ roles.
func (s *FormulaService) GetFormula(
	ctx context.Context, userID, bdbID uuid.UUID, ref string,
) (*domain.Formula, error) {
	ctx, span := tracer.Start(ctx, "FormulaService.GetFormula",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("formula.ref", ref),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}
	return s.fetchFormulaByRef(ctx, db.ID.String(), ref)
}

// ListFormulas returns the bdb's app-facing formula listing, ordered by display_order then name.
// Hidden formulas are included only for callers who can write formulas (they get the app's
// "show hidden" affordance); viewers never see them listed. Hidden is presentation-only —
// get-by-ref and run stay read-gated for everyone.
func (s *FormulaService) ListFormulas(
	ctx context.Context, userID, bdbID uuid.UUID,
) ([]*domain.Formula, error) {
	ctx, span := tracer.Start(ctx, "FormulaService.ListFormulas",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()

	includeHidden := true
	if err := s.dbSvc.CheckDatabasePermission(ctx, userID, bdbID, authz.DBPermWrite); err != nil {
		if !errors.Is(err, domain.ErrAccessDenied) {
			return nil, err
		}
		includeHidden = false
	}
	return s.listFormulas(ctx, userID, bdbID, includeHidden)
}

// ListFormulasIncludingHidden returns every formula in the bdb regardless of the hidden flag,
// ordered by display_order then name. This is the assistant-facing listing: hiding is an
// app-presentation concept, so agent surfaces always see everything (read permission suffices).
func (s *FormulaService) ListFormulasIncludingHidden(
	ctx context.Context, userID, bdbID uuid.UUID,
) ([]*domain.Formula, error) {
	ctx, span := tracer.Start(ctx, "FormulaService.ListFormulasIncludingHidden",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
		),
	)
	defer span.End()

	return s.listFormulas(ctx, userID, bdbID, true)
}

// listFormulas authorizes the read and fetches the formula rows, filtering hidden ones out unless
// includeHidden is set.
func (s *FormulaService) listFormulas(
	ctx context.Context, userID, bdbID uuid.UUID, includeHidden bool,
) ([]*domain.Formula, error) {
	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf("SELECT %s FROM %s", formulaColumns, domain.FormulasTable)
	if !includeHidden {
		query += " WHERE hidden = 0"
	}
	query += " ORDER BY display_order ASC, name ASC"

	result, err := s.executor.ExecuteInternal(ctx, db.ID.String(), query, nil)
	if err != nil {
		return nil, fmt.Errorf("listing formulas: %w", err)
	}
	if result == nil || len(result.Rows) == 0 {
		return []*domain.Formula{}, nil
	}

	formulas := make([]*domain.Formula, 0, len(result.Rows))
	for _, row := range result.Rows {
		formula, err := scanFormulaRow(row)
		if err != nil {
			return nil, err
		}
		formulas = append(formulas, formula)
	}
	return formulas, nil
}

// DeleteFormula removes the formula resolved by name or UUID. Attached actions are
// removed via FK cascade. Returns ErrFormulaNotFound if it does not exist.
func (s *FormulaService) DeleteFormula(ctx context.Context, userID, bdbID uuid.UUID, ref string) error {
	ctx, span := tracer.Start(ctx, "FormulaService.DeleteFormula",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("formula.ref", ref),
		),
	)
	defer span.End()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermWrite)
	if err != nil {
		return err
	}
	databaseID := db.ID.String()

	existing, err := s.fetchFormulaByRef(ctx, databaseID, ref)
	if err != nil {
		return err
	}

	// An attached action referenced by a slot of its parent view's display cannot be deleted out
	// from under it — clear the slot first. (Deleting the parent view itself is fine: the display
	// config dies with the row and the attached formulas cascade.)
	if existing.AttachedToFormulaID != nil {
		parent, perr := s.fetchFormulaByRef(ctx, databaseID, existing.AttachedToFormulaID.String())
		if perr != nil {
			return fmt.Errorf("fetching parent view: %w", perr)
		}
		if slot, ok := parent.Display.SlotReferencing(existing.ID.String()); ok {
			return fmt.Errorf("%w: action %q fills the %q slot of view %q's display — clear the slot first",
				domain.ErrFormulaDisplayInvalid, existing.Name, slot, parent.Name)
		}
	}

	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE id = ?", domain.FormulasTable)
	if _, err := s.executor.ExecuteInternal(ctx, databaseID, deleteSQL,
		[]domain.TypedValue{domain.TextValue(existing.ID.String())}); err != nil {
		return fmt.Errorf("deleting formula: %w", err)
	}
	return nil
}

// --- internal helpers ---

// classifyFormulaSQL classifies sql against the database's schema and reduces the result to a
// FormulaKind plus its statement verb, the SQL's bind-parameter names, and — for a view — the
// SELECT's result column names. Reserved-table denials pass through; other parse failures surface
// as ErrFormulaSQLInvalid.
func (s *FormulaService) classifyFormulaSQL(
	ctx context.Context, databaseID, sql string,
) (domain.FormulaKind, domain.StatementVerb, []string, []string, error) {
	var (
		stmts      []domain.ClassifiedStatement
		paramNames []string
		sqlErr     error
	)
	// The connection's own failures (opening the file, a cancelled request) pass through as they are: only sqlErr
	// describes the SQL.
	if err := s.executor.WithConn(ctx, databaseID, func(h ParseHandle) error {
		stmts, sqlErr = s.classifier.Classify(h, sql)
		if sqlErr != nil {
			return nil
		}
		// An action may hold several statements and a prepare only reports its own statement's bind
		// parameters, so every statement is parsed and the names concatenated for the schema
		// cross-check (set-based, so a name repeated across statements is harmless).
		for _, cs := range stmts {
			res, perr := h.Parse(cs.SQL)
			if perr != nil {
				sqlErr = perr
				return nil
			}
			paramNames = append(paramNames, res.ParamNames...)
		}
		return nil
	}); err != nil {
		return "", "", nil, nil, err
	}
	if sqlErr != nil {
		if errors.Is(sqlErr, domain.ErrReservedTable) {
			return "", "", nil, nil, sqlErr
		}
		// Classification prepares the SQL against the live schema, so a failure here means the
		// SQL doesn't parse or references something that doesn't exist (e.g. an unknown table) —
		// distinct from a well-formed statement of the wrong kind (DeriveFormulaKind's job below).
		return "", "", nil, nil, fmt.Errorf("%w: %w", domain.ErrFormulaSQLInvalid, sqlErr)
	}
	kind, err := domain.DeriveFormulaKind(stmts)
	if err != nil {
		return "", "", nil, nil, err
	}
	var resultCols []string
	if kind == domain.FormulaKindView && len(stmts) > 0 {
		resultCols = stmts[0].ResultColumns
	}
	return kind, domain.DeriveStatementVerb(stmts), paramNames, resultCols, nil
}

// resolveDisplayOrder returns explicit when non-nil, otherwise max(display_order)+1
// among siblings sharing the same attached_to_formula_id (NULL for top-level).
func (s *FormulaService) resolveDisplayOrder(
	ctx context.Context, databaseID string, explicit *int, attachedTo *uuid.UUID,
) (int, error) {
	if explicit != nil {
		return *explicit, nil
	}
	result, err := s.executor.ExecuteInternal(ctx, databaseID,
		fmt.Sprintf("SELECT COALESCE(MAX(display_order), -1) + 1 FROM %s WHERE attached_to_formula_id IS ?",
			domain.FormulasTable),
		[]domain.TypedValue{attachArg(attachedTo)},
	)
	if err != nil {
		return 0, fmt.Errorf("resolving display order: %w", err)
	}
	if result == nil || len(result.Rows) == 0 || len(result.Rows[0]) == 0 {
		return 0, nil
	}
	if n, ok := result.Rows[0][0].Value.(int64); ok {
		return int(n), nil
	}
	return 0, nil
}

// verifyAttachTarget checks that the attach target exists and is a view, and that the attaching
// formula is a supported child: an action (rendered inside the parent view, binding its row
// selection) or a row-detail view (run from a clicked row, so its params must bind from the row
// alone).
func (s *FormulaService) verifyAttachTarget(
	ctx context.Context, databaseID string, targetID uuid.UUID, kind domain.FormulaKind,
	params domain.ParamSchema,
) error {
	target, err := s.fetchFormulaByRef(ctx, databaseID, targetID.String())
	if err != nil {
		if errors.Is(err, domain.ErrFormulaNotFound) {
			return fmt.Errorf("%w: %s", domain.ErrFormulaAttachTargetNotFound, targetID)
		}
		return fmt.Errorf("verifying attach target: %w", err)
	}
	if target.Kind != domain.FormulaKindView {
		return fmt.Errorf("%w: a formula can only attach to a view", domain.ErrFormulaAttachConflict)
	}
	if kind == domain.FormulaKindAction {
		return nil
	}
	return domain.ValidateRowDetailParams(params)
}

// verifyKindStable rejects a SQL change that would flip a formula's kind while an attachment
// depends on it: a view with attached formulas must stay a view (its children bind its row
// selection), and an attached formula must keep its own kind — an action stays a write, a
// row-detail view stays a read. Detach first to change either.
func (s *FormulaService) verifyKindStable(
	ctx context.Context, databaseID string, existing *domain.Formula, newKind domain.FormulaKind,
) error {
	if existing.AttachedToFormulaID != nil && newKind != existing.Kind {
		return fmt.Errorf("%w: an attached formula must keep its kind — detach it first",
			domain.ErrFormulaAttachConflict)
	}
	if newKind == domain.FormulaKindView {
		return nil
	}
	children, err := s.countAttachedFormulas(ctx, databaseID, existing.ID)
	if err != nil {
		return err
	}
	if children > 0 {
		return fmt.Errorf("%w: this view has attached formulas — detach them before changing it to a write statement",
			domain.ErrFormulaAttachConflict)
	}
	return nil
}

// countAttachedFormulas returns how many formulas attach to the given formula.
func (s *FormulaService) countAttachedFormulas(
	ctx context.Context, databaseID string, id uuid.UUID,
) (int, error) {
	result, err := s.executor.ExecuteInternal(ctx, databaseID,
		fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE attached_to_formula_id = ?", domain.FormulasTable),
		[]domain.TypedValue{domain.TextValue(id.String())},
	)
	if err != nil {
		return 0, fmt.Errorf("counting attached actions: %w", err)
	}
	if result != nil && len(result.Rows) > 0 && len(result.Rows[0]) > 0 {
		if n, ok := result.Rows[0][0].Value.(int64); ok {
			return int(n), nil
		}
	}
	return 0, nil
}

// verifySlotShapeKept rejects an update to an attached action that would break the display slot
// its parent view references it from — the effective (patched) kind, verb, and params must still
// satisfy the slot's shape rules.
func (s *FormulaService) verifySlotShapeKept(
	ctx context.Context, databaseID string, existing *domain.Formula,
	kind domain.FormulaKind, verb domain.StatementVerb, params domain.ParamSchema,
) error {
	parent, err := s.fetchFormulaByRef(ctx, databaseID, existing.AttachedToFormulaID.String())
	if err != nil {
		return fmt.Errorf("fetching parent view: %w", err)
	}
	slot, ok := parent.Display.SlotReferencing(existing.ID.String())
	if !ok {
		return nil
	}
	shadow := *existing
	shadow.Kind = kind
	shadow.StatementVerb = verb
	shadow.Params = params
	if err := domain.ValidateDisplaySlotAction(slot, parent.Display.Mode, &shadow,
		parent.AttachedToFormulaID != nil); err != nil {
		return fmt.Errorf("%w — remove %q from the %q slot of view %q's display first",
			err, existing.Name, slot, parent.Name)
	}
	return nil
}

// fetchAttachedFormulas returns the formulas attached to the given view.
func (s *FormulaService) fetchAttachedFormulas(
	ctx context.Context, databaseID string, viewID uuid.UUID,
) ([]*domain.Formula, error) {
	result, err := s.executor.ExecuteInternal(ctx, databaseID,
		fmt.Sprintf("SELECT %s FROM %s WHERE attached_to_formula_id = ?", formulaColumns, domain.FormulasTable),
		[]domain.TypedValue{domain.TextValue(viewID.String())},
	)
	if err != nil {
		return nil, fmt.Errorf("fetching attached formulas: %w", err)
	}
	if result == nil {
		return nil, nil
	}
	out := make([]*domain.Formula, 0, len(result.Rows))
	for _, row := range result.Rows {
		f, serr := scanFormulaRow(row)
		if serr != nil {
			return nil, serr
		}
		out = append(out, f)
	}
	return out, nil
}

// resolveDisplaySlots resolves each filled action slot against the view's attached formulas and
// checks the slot's required action shape, returning the display with slot refs normalized to
// formula UUIDs so a later rename never breaks a slot. viewAttached marks a display carried by a
// row-detail view, which flips the add slot's parent-row binding rule.
func resolveDisplaySlots(
	display domain.FormulaDisplay, attached []*domain.Formula, viewAttached bool,
) (domain.FormulaDisplay, error) {
	// Slots are rebuilt from scratch so the list slots (whose Set appends) end up with exactly the
	// resolved entries, in the declared order.
	normalized := display
	normalized.Slots = domain.FormulaDisplaySlots{}
	seen := make(map[uuid.UUID]domain.DisplaySlot)
	// The built-in menu entries a row_actions ref has positioned; each may be placed once.
	placed := make(map[uuid.UUID]bool)
	for slot, ref := range display.Slots.All() {
		action, ok := findAttachedFormula(attached, ref)
		if !ok {
			return display, fmt.Errorf(
				"%w: slot %q: no formula %q is attached to this view — create it attached to the view, then set it",
				domain.ErrFormulaDisplayInvalid, slot, ref)
		}
		if prev, dup := seen[action.ID]; dup {
			// The single-ref slots resolve before the lists, so a repeat met here on row_actions names a
			// slot already validated under its own rules: it positions that entry, nothing more.
			if slot == domain.DisplaySlotRowActions && prev.PlacesInRowMenu() && !placed[action.ID] {
				placed[action.ID] = true
				normalized.Slots.Set(slot, action.ID.String())
				continue
			}
			if placed[action.ID] {
				return display, fmt.Errorf("%w: row_actions places %q twice",
					domain.ErrFormulaDisplayInvalid, action.Name)
			}
			return display, fmt.Errorf(
				"%w: formula %q already fills slot %q — an attached formula holds at most one role",
				domain.ErrFormulaDisplayInvalid, action.Name, prev)
		}
		if err := domain.ValidateDisplaySlotAction(slot, display.Mode, action, viewAttached); err != nil {
			return display, err
		}
		seen[action.ID] = slot
		normalized.Slots.Set(slot, action.ID.String())
	}
	return normalized, nil
}

// findAttachedFormula resolves ref as a UUID or a name among the given formulas.
func findAttachedFormula(attached []*domain.Formula, ref string) (*domain.Formula, bool) {
	if id, err := uuid.Parse(ref); err == nil {
		for _, f := range attached {
			if f.ID == id {
				return f, true
			}
		}
		return nil, false
	}
	for _, f := range attached {
		if f.Name == ref {
			return f, true
		}
	}
	return nil, false
}

// verifyLookupTargets checks each lookup parameter's table and columns against the live schema —
// group fields included — so a formula whose picker could never be populated is rejected when it
// is saved instead of failing in the app.
func (s *FormulaService) verifyLookupTargets(
	ctx context.Context, databaseID string, schema domain.ParamSchema,
) error {
	for _, p := range schema {
		if err := s.verifyLookupTarget(ctx, databaseID, p); err != nil {
			return err
		}
		for _, field := range p.Fields {
			if err := s.verifyLookupTarget(ctx, databaseID, field); err != nil {
				return err
			}
		}
	}
	return nil
}

// verifyLookupTarget checks one parameter's lookup table and columns against the live schema.
func (s *FormulaService) verifyLookupTarget(
	ctx context.Context, databaseID string, p domain.FormulaParam,
) error {
	if p.Source != domain.ParamSourceLookup || p.Lookup == nil {
		return nil
	}
	if !safeTableName.MatchString(p.Lookup.Table) {
		return fmt.Errorf("%w: invalid lookup table name %q", domain.ErrFormulaParamInvalid, p.Lookup.Table)
	}
	desc, err := s.querySvc.describeTableInDatabase(ctx, databaseID, p.Lookup.Table)
	if err != nil {
		return fmt.Errorf("describing lookup table %q: %w", p.Lookup.Table, err)
	}
	if len(desc.Columns) == 0 {
		return fmt.Errorf("%w: lookup table %q does not exist", domain.ErrFormulaParamInvalid, p.Lookup.Table)
	}
	cols := []string{p.Lookup.LabelColumn, p.Lookup.ValueColumn}
	for _, col := range p.Lookup.Scope {
		cols = append(cols, col)
	}
	for _, col := range p.Lookup.Fill {
		cols = append(cols, col)
	}
	for _, col := range cols {
		if !slices.ContainsFunc(desc.Columns, func(c domain.TableColumn) bool { return c.Name == col }) {
			return fmt.Errorf("%w: lookup table %q has no column %q",
				domain.ErrFormulaParamInvalid, p.Lookup.Table, col)
		}
	}
	return nil
}

// validateBulkPlaceholder checks, when the schema declares a selection-bulk parameter, that the
// SQL holds exactly one expandable :rowids token — so a formula that would fail the run-time
// expansion on every run is rejected at save time instead.
func validateBulkPlaceholder(schema domain.ParamSchema, sql string) error {
	for _, p := range schema {
		if p.Source == domain.ParamSourceSelectionBulk {
			if err := domain.ValidateRowIDsPlaceholder(sql); err != nil {
				return fmt.Errorf("%w: %w", domain.ErrFormulaParamInvalid, err)
			}
			return nil
		}
	}
	return nil
}

// fetchFormulaByRef resolves a ref as either a UUID or a slug name and returns the formula.
func (s *FormulaService) fetchFormulaByRef(ctx context.Context, databaseID, ref string) (*domain.Formula, error) {
	var (
		whereCol string
		arg      domain.TypedValue
	)
	if id, err := uuid.Parse(ref); err == nil {
		whereCol = "id"
		arg = domain.TextValue(id.String())
	} else {
		whereCol = "name"
		arg = domain.TextValue(ref)
	}

	result, err := s.executor.ExecuteInternal(ctx, databaseID,
		fmt.Sprintf("SELECT %s FROM %s WHERE %s = ?", formulaColumns, domain.FormulasTable, whereCol),
		[]domain.TypedValue{arg},
	)
	if err != nil {
		return nil, fmt.Errorf("fetching formula: %w", err)
	}
	if result == nil || len(result.Rows) == 0 {
		return nil, fmt.Errorf("%w: %s", domain.ErrFormulaNotFound, ref)
	}
	return scanFormulaRow(result.Rows[0])
}

// scanFormulaRow maps a single _formulas result row into a *domain.Formula.
func scanFormulaRow(row []domain.TypedValue) (*domain.Formula, error) {
	if len(row) < 14 {
		return nil, fmt.Errorf("%w: formula row has %d columns, want 14", domain.ErrQueryExecution, len(row))
	}
	idStr, _ := row[0].Value.(string)
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid formula id %q", domain.ErrQueryExecution, idStr)
	}
	name, _ := row[1].Value.(string)
	description, _ := row[2].Value.(string)
	sqlText, _ := row[3].Value.(string)
	kind, _ := row[4].Value.(string)
	paramsJSON, _ := row[5].Value.(string)
	params, err := unmarshalParams(paramsJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: decoding formula params: %w", domain.ErrQueryExecution, err)
	}

	var attachedTo *uuid.UUID
	if attStr, ok := row[6].Value.(string); ok && attStr != "" {
		if att, perr := uuid.Parse(attStr); perr == nil {
			attachedTo = &att
		}
	}

	var order int
	if n, ok := row[7].Value.(int64); ok {
		order = int(n)
	}

	verb, _ := row[10].Value.(string)

	var display *domain.FormulaDisplay
	if displayJSON, ok := row[11].Value.(string); ok && displayJSON != "" {
		display, err = unmarshalDisplay(displayJSON)
		if err != nil {
			return nil, fmt.Errorf("%w: decoding formula display: %w", domain.ErrQueryExecution, err)
		}
	}
	hidden := false
	if n, ok := row[12].Value.(int64); ok {
		hidden = n != 0
	}

	// Rows written before the label column existed read back NULL, which the assertion leaves empty —
	// the same state as an unset label, so the app falls back to the name.
	label, _ := row[13].Value.(string)

	return &domain.Formula{
		ID:                  id,
		Name:                name,
		Label:               label,
		Description:         description,
		SQL:                 sqlText,
		Kind:                domain.FormulaKind(kind),
		StatementVerb:       domain.StatementVerb(verb),
		Params:              params,
		Display:             display,
		Hidden:              hidden,
		AttachedToFormulaID: attachedTo,
		DisplayOrder:        order,
		CreatedAt:           domain.ParseSQLiteTimestamp(row[8].String()),
		UpdatedAt:           domain.ParseSQLiteTimestamp(row[9].String()),
	}, nil
}

// attachArg renders a nullable attach reference as a bind argument.
func attachArg(id *uuid.UUID) domain.TypedValue {
	if id == nil {
		return domain.NullValue()
	}
	return domain.TextValue(id.String())
}

// boolArg renders a bool as the SQLite 0/1 integer bind argument.
func boolArg(b bool) domain.TypedValue {
	if b {
		return domain.IntegerValue(1)
	}
	return domain.IntegerValue(0)
}

// storedParam is the JSON shape persisted in _formulas.params (domain types carry
// no serialization tags, so the service owns the wire format).
type storedParam struct {
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Source    string         `json:"source"`
	Optional  bool           `json:"optional,omitempty"`
	Lookup    *storedLookup  `json:"lookup,omitempty"`
	Hint      string         `json:"hint,omitempty"`
	Multiline bool           `json:"multiline,omitempty"`
	Inline    bool           `json:"inline,omitempty"`
	Multiple  bool           `json:"multiple,omitempty"`
	Options   []storedOption `json:"options,omitempty"`
	Default   string         `json:"default,omitempty"`
	Defaults  []string       `json:"defaults,omitempty"`
	When      *storedWhen    `json:"when,omitempty"`
	Fields    []storedParam  `json:"fields,omitempty"`
}

// storedOption is the persisted shape of one closed-set entry: the bound value and its shown label.
type storedOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// UnmarshalJSON also accepts the pre-label persisted shape — a bare value string — folding it into
// the object form (label = value), so formulas stored before labels existed read without migration.
func (o *storedOption) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*o = storedOption{Value: s, Label: s}
		return nil
	}
	type plain storedOption
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*o = storedOption(p)
	return nil
}

// storedLookup is the persisted shape of a lookup parameter's options source.
type storedLookup struct {
	Table            string            `json:"table"`
	LabelColumn      string            `json:"label_column"`
	ValueColumn      string            `json:"value_column"`
	ExcludeSelection bool              `json:"exclude_selection,omitempty"`
	AllowNew         bool              `json:"allow_new,omitempty"`
	Scope            map[string]string `json:"scope,omitempty"`
	Fill             map[string]string `json:"fill,omitempty"`
}

// storedWhen is the persisted shape of a parameter's visibility condition.
type storedWhen struct {
	Param string `json:"param"`
	Op    string `json:"op,omitempty"`
	Value string `json:"value"`
}

// marshalParams encodes a parameter schema as the JSON stored in _formulas.params.
func marshalParams(params domain.ParamSchema) (string, error) {
	out := make([]storedParam, len(params))
	for i, p := range params {
		out[i] = toStoredParam(p)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// toStoredParam converts one domain parameter — group fields included — into its persisted shape.
func toStoredParam(p domain.FormulaParam) storedParam {
	var lookup *storedLookup
	if p.Lookup != nil {
		lookup = &storedLookup{
			Table:            p.Lookup.Table,
			LabelColumn:      p.Lookup.LabelColumn,
			ValueColumn:      p.Lookup.ValueColumn,
			ExcludeSelection: p.Lookup.ExcludeSelection,
			AllowNew:         p.Lookup.AllowNew,
			Scope:            p.Lookup.Scope,
			Fill:             p.Lookup.Fill,
		}
	}
	var when *storedWhen
	if p.When != nil {
		when = &storedWhen{Param: p.When.Param, Op: p.When.Op, Value: p.When.Value}
	}
	sp := storedParam{
		Name: p.Name, Type: p.Type, Source: string(p.Source), Optional: p.Optional, Lookup: lookup, Hint: p.Hint,
		Multiline: p.Multiline, Inline: p.Inline, Multiple: p.Multiple, Options: toStoredOptions(p.Options),
		Default: p.Default, Defaults: p.Defaults, When: when,
	}
	for _, field := range p.Fields {
		sp.Fields = append(sp.Fields, toStoredParam(field))
	}
	return sp
}

// storedDisplay is the JSON shape persisted in _formulas.display (domain types carry no
// serialization tags, so the service owns the wire format). The flat *_column fields are the
// pre-columns legacy shape: still read (normalized into Columns), never written.
type storedDisplay struct {
	Mode        string                `json:"mode"`
	Columns     []storedDisplayColumn `json:"columns,omitempty"`
	TitleColumn string                `json:"title_column,omitempty"`
	BodyColumn  string                `json:"body_column,omitempty"`
	CheckColumn string                `json:"check_column,omitempty"`
	Slots       *storedDisplaySlots   `json:"slots,omitempty"`
	Window      *storedDisplayWindow  `json:"window,omitempty"`
}

// storedDisplayWindow is the persisted calendar window: weekday names for a weekly one, a start day and a
// length for a fixed range.
type storedDisplayWindow struct {
	WeekStart string `json:"week_start,omitempty"`
	WeekEnd   string `json:"week_end,omitempty"`
	Start     string `json:"start,omitempty"`
	Days      int    `json:"days,omitempty"`
}

// storedDisplayColumn is one persisted per-column presentation entry.
type storedDisplayColumn struct {
	Column string `json:"column"`
	Role   string `json:"role,omitempty"`
	Format string `json:"format,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

// storedDisplaySlots is the persisted slot → attached-formula UUID mapping.
type storedDisplaySlots struct {
	Add        string   `json:"add,omitempty"`
	Toggle     string   `json:"toggle,omitempty"`
	Edit       string   `json:"edit,omitempty"`
	Delete     string   `json:"delete,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	Reorder    string   `json:"reorder,omitempty"`
	Move       string   `json:"move,omitempty"`
	Bulk       []string `json:"bulk,omitempty"`
	RowActions []string `json:"row_actions,omitempty"`
}

// marshalDisplayArg encodes an optional display config as the _formulas.display bind argument
// (SQL NULL when nil — table mode is never stored explicitly).
func marshalDisplayArg(d *domain.FormulaDisplay) (domain.TypedValue, error) {
	if d == nil {
		return domain.NullValue(), nil
	}
	sd := storedDisplay{Mode: string(d.Mode)}
	for _, c := range d.Columns {
		sd.Columns = append(sd.Columns, storedDisplayColumn{
			Column: c.Column, Role: string(c.Role), Format: string(c.Format), Hidden: c.Hidden,
		})
	}
	if !d.Slots.IsEmpty() {
		sd.Slots = &storedDisplaySlots{
			Add:        d.Slots.Add,
			Toggle:     d.Slots.Toggle,
			Edit:       d.Slots.Edit,
			Delete:     d.Slots.Delete,
			Detail:     d.Slots.Detail,
			Reorder:    d.Slots.Reorder,
			Move:       d.Slots.Move,
			Bulk:       d.Slots.Bulk,
			RowActions: d.Slots.RowActions,
		}
	}
	if w := d.Window; w != nil {
		sd.Window = &storedDisplayWindow{
			WeekStart: string(w.WeekStart), WeekEnd: string(w.WeekEnd), Start: w.Start, Days: w.Days,
		}
	}
	b, err := json.Marshal(sd)
	if err != nil {
		return domain.TypedValue{}, err
	}
	return domain.TextValue(string(b)), nil
}

// unmarshalDisplay decodes the JSON stored in _formulas.display.
func unmarshalDisplay(s string) (*domain.FormulaDisplay, error) {
	var sd storedDisplay
	if err := json.Unmarshal([]byte(s), &sd); err != nil {
		return nil, err
	}
	d := &domain.FormulaDisplay{Mode: domain.DisplayMode(sd.Mode)}
	for _, c := range sd.Columns {
		d.Columns = append(d.Columns, domain.FormulaDisplayColumn{
			Column: c.Column, Role: domain.DisplayColumnRole(c.Role),
			Format: domain.DisplayColumnFormat(c.Format), Hidden: c.Hidden,
		})
	}
	// Configs stored before per-column entries carried flat role fields; normalize them on read so
	// the domain only ever sees the columns shape (the next write persists it).
	if len(d.Columns) == 0 {
		for _, legacy := range []struct {
			role domain.DisplayColumnRole
			col  string
		}{
			{domain.DisplayRoleTitle, sd.TitleColumn},
			{domain.DisplayRoleBody, sd.BodyColumn},
			{domain.DisplayRoleCheck, sd.CheckColumn},
		} {
			if legacy.col != "" {
				d.Columns = append(d.Columns, domain.FormulaDisplayColumn{Column: legacy.col, Role: legacy.role})
			}
		}
	}
	if sd.Slots != nil {
		d.Slots = domain.FormulaDisplaySlots{
			Add:        sd.Slots.Add,
			Toggle:     sd.Slots.Toggle,
			Edit:       sd.Slots.Edit,
			Delete:     sd.Slots.Delete,
			Detail:     sd.Slots.Detail,
			Reorder:    sd.Slots.Reorder,
			Move:       sd.Slots.Move,
			Bulk:       sd.Slots.Bulk,
			RowActions: sd.Slots.RowActions,
		}
	}
	if w := sd.Window; w != nil {
		d.Window = &domain.FormulaDisplayWindow{
			WeekStart: domain.DisplayWeekday(w.WeekStart), WeekEnd: domain.DisplayWeekday(w.WeekEnd),
			Start: w.Start, Days: w.Days,
		}
	}
	return d, nil
}

// unmarshalParams decodes the JSON stored in _formulas.params into a parameter schema.
func unmarshalParams(s string) (domain.ParamSchema, error) {
	if s == "" {
		return domain.ParamSchema{}, nil
	}
	var in []storedParam
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		return nil, err
	}
	out := make(domain.ParamSchema, len(in))
	for i, p := range in {
		out[i] = fromStoredParam(p)
	}
	return out, nil
}

// fromStoredParam converts one persisted parameter — group fields included — back into its domain
// shape.
func fromStoredParam(p storedParam) domain.FormulaParam {
	var lookup *domain.LookupConfig
	if p.Lookup != nil {
		lookup = &domain.LookupConfig{
			Table:            p.Lookup.Table,
			LabelColumn:      p.Lookup.LabelColumn,
			ValueColumn:      p.Lookup.ValueColumn,
			ExcludeSelection: p.Lookup.ExcludeSelection,
			AllowNew:         p.Lookup.AllowNew,
			Scope:            p.Lookup.Scope,
			Fill:             p.Lookup.Fill,
		}
	}
	var when *domain.ParamWhen
	if p.When != nil {
		when = &domain.ParamWhen{Param: p.When.Param, Op: p.When.Op, Value: p.When.Value}
	}
	fp := domain.FormulaParam{
		Name: p.Name, Type: p.Type, Source: domain.ParamSource(p.Source), Optional: p.Optional, Lookup: lookup,
		Hint: p.Hint, Multiline: p.Multiline, Inline: p.Inline, Multiple: p.Multiple,
		Options: fromStoredOptions(p.Options), Default: p.Default, Defaults: p.Defaults, When: when,
	}
	for _, field := range p.Fields {
		fp.Fields = append(fp.Fields, fromStoredParam(field))
	}
	return fp
}

// toStoredOptions converts a domain option set into its persisted object entries.
func toStoredOptions(options []domain.FormulaOption) []storedOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]storedOption, len(options))
	for i, o := range options {
		out[i] = storedOption{Value: o.Value, Label: o.Label}
	}
	return out
}

// fromStoredOptions converts persisted option entries back into the domain set, folding an absent
// label to the value.
func fromStoredOptions(options []storedOption) []domain.FormulaOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]domain.FormulaOption, len(options))
	for i, o := range options {
		label := o.Label
		if label == "" {
			label = o.Value
		}
		out[i] = domain.FormulaOption{Value: o.Value, Label: label}
	}
	return out
}
