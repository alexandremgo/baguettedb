package service

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// FormulaRunInput carries the caller-supplied parameter values for a run. Params holds every declared
// parameter value keyed by name: scalars for the prompt / selection-single / pagination sources, and an
// array of row IDs for the selection-bulk source (the reserved :rowids placeholder).
type FormulaRunInput struct {
	Params map[string]any
}

// FormulaPagination is the keyset-pagination metadata returned for a paginated view run, so any
// consumer (the app or the AI assistant over MCP) knows whether to fetch more and where to resume.
type FormulaPagination struct {
	// HasMore reports that at least one row exists beyond the returned page.
	HasMore bool
	// NextCursor is the last returned row's rowid, to pass as :cursor for the next page. Nil only
	// when the page is empty (a paginated view without a usable rowid column fails its run instead).
	NextCursor *int64
}

// FormulaRunResult is a formula run's outcome: the query result, plus pagination metadata for a
// paginated view (nil for non-paginated views and for actions).
type FormulaRunResult struct {
	Result     *domain.QueryResult
	Pagination *FormulaPagination
	// Kind is the formula's kind: a view's Result holds its rows, an action's the rows it changed.
	Kind domain.FormulaKind
	// StatementCount is how many SQL statements the run executed. The affected-row count sums across
	// all of them, so callers need this to know whether that total maps one-to-one onto a user-visible
	// change or mixes bookkeeping rows from a compound action.
	StatementCount int
}

// paginationPlan records that a view run paginates and its effective page size (before the
// over-fetch-by-one), so the run can detect a next page and trim the surplus row.
type paginationPlan struct {
	enabled bool
	limit   int64
}

// RunFormulaFromStrings runs the formula ref as RunFormula does, its parameter values given as strings, as a command
// line carries them, and converted to each declared parameter's type first: text, dates and times as given, numbers
// parsed, booleans strictly "true" or "false", a multi-select, bulk row selection or reorder as a JSON array, a group
// as a JSON array of row objects. An undeclared parameter, like any value its parameter refuses, is a
// *domain.ParamValueError.
func (s *FormulaService) RunFormulaFromStrings(
	ctx context.Context, userID, bdbID uuid.UUID, ref string, rawParams map[string]string,
) (*FormulaRunResult, error) {
	formula, err := s.GetFormula(ctx, userID, bdbID, ref)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]domain.FormulaParam, len(formula.Params))
	for _, p := range formula.Params {
		byName[p.Name] = p
	}
	params := make(map[string]any, len(rawParams))
	for name, raw := range rawParams {
		p, ok := byName[name]
		if !ok {
			return nil, unknownParamErr(name, formula.Params)
		}
		if params[name], err = coerceParamValueFromString(p, raw); err != nil {
			return nil, err
		}
	}
	return s.RunFormula(ctx, userID, bdbID, ref, FormulaRunInput{Params: params})
}

// RunFormula resolves the formula by ref, validates the caller's parameter values against its stored schema,
// expands the :rowids bulk selection, binds every parameter positionally, and runs the result through
// the shared execute path — so class-based authorization, the change log, and undo all apply exactly as for raw SQL.
// A view runs as a read; an action runs as a write (a viewer's action run is rejected by the execute path, not here).
func (s *FormulaService) RunFormula(
	ctx context.Context, userID, bdbID uuid.UUID, ref string, input FormulaRunInput,
) (*FormulaRunResult, error) {
	ctx, span := tracer.Start(ctx, "FormulaService.RunFormula",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("bdb.id", bdbID.String()),
			attribute.String("formula.ref", ref),
		),
	)
	defer span.End()

	// The whole run counts as one of the caller's in-flight requests: parameter binding already needs the bdb's
	// connection, so the slot is taken here rather than only around the final execute.
	ctx, release, err := s.querySvc.acquireSlot(ctx, userID)
	if err != nil {
		return nil, err
	}
	defer release()

	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}
	databaseID := db.ID.String()

	formula, err := s.fetchFormulaByRef(ctx, databaseID, ref)
	if err != nil {
		return nil, err
	}

	if input.Params == nil {
		input.Params = make(map[string]any)
	}
	if err := s.resolveMultiDefaults(ctx, databaseID, formula.Params, input.Params); err != nil {
		return nil, err
	}

	// A view paginates (over-fetch + trim + metadata); an action never does, so it must bind the real
	// :limit rather than the over-fetched one — guard the over-fetch on the kind.
	paginate := formula.Kind == domain.FormulaKindView
	scalars, rowIDs, plan, err := resolveFormulaParams(formula.Params, input, paginate, 0)
	if err != nil {
		return nil, err
	}

	sql := formula.SQL
	if len(rowIDs) > 0 {
		expanded, eerr := domain.ExpandRowIDsPlaceholder(sql, len(rowIDs))
		if eerr != nil {
			return nil, fmt.Errorf("%w: %w", domain.ErrFormulaParamInvalid, eerr)
		}
		sql = expanded
	}

	argsPerStmt, statementCount, err := s.bindFormulaArgs(ctx, databaseID, sql, scalars, rowIDs)
	if err != nil {
		return nil, err
	}

	intent := domain.IntentQuery
	if formula.Kind == domain.FormulaKindAction {
		intent = domain.IntentExecute
	}

	// Bound the run by the per-run deadline (when configured). On expiry the ctx cancels, the adapter
	// interrupts SQLite, and Execute errors — the ctx checks below then attribute the failure to our
	// deadline only when it actually fired and the caller's own ctx is still live (a caller
	// cancellation or an inherited shorter deadline must not masquerade as the formula timeout).
	runCtx := ctx
	if s.runTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, s.runTimeout)
		defer cancel()
	}
	result, err := s.querySvc.ExecuteStatements(runCtx, userID, bdbID, sql, argsPerStmt, intent)
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			s.logger.Warn().
				Str("bdb_id", bdbID.String()).
				Str("formula_id", formula.ID.String()).
				Str("formula_name", formula.Name).
				Dur("run_timeout", s.runTimeout).
				Msg("Formula run exceeded the per-run deadline")
			return nil, domain.ErrFormulaRunTimeout
		}
		return nil, err
	}

	runResult := &FormulaRunResult{Result: result, StatementCount: statementCount, Kind: formula.Kind}
	if plan.enabled {
		pagination, perr := paginateResult(result, plan.limit)
		if perr != nil {
			return nil, perr
		}
		runResult.Pagination = pagination
	}
	return runResult, nil
}

// paginateResult turns an over-fetched view result into the returned page plus its metadata: it
// trims the surplus row used to detect a next page, sets HasMore, and reads NextCursor from the
// last returned row's rowid. A paginated view that returns no usable integer "rowid" column cannot
// be resumed, so the run errors (surfacing the misauthored view on its very first run) rather than
// letting consumers silently truncate at page one.
func paginateResult(result *domain.QueryResult, effLimit int64) (*FormulaPagination, error) {
	idx := -1
	for i, c := range result.Columns {
		if c.Name == "rowid" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("%w: it declares :cursor/:limit but returns no \"rowid\" column",
			domain.ErrFormulaViewNotPageable)
	}

	hasMore := int64(len(result.Rows)) > effLimit
	if hasMore {
		result.Rows = result.Rows[:effLimit]
	}
	p := &FormulaPagination{HasMore: hasMore}
	if len(result.Rows) > 0 {
		last := result.Rows[len(result.Rows)-1]
		if idx >= len(last) {
			return nil, fmt.Errorf("%w: its final row is missing the \"rowid\" column",
				domain.ErrFormulaViewNotPageable)
		}
		v, ok := last[idx].Value.(int64)
		if !ok {
			return nil, fmt.Errorf("%w: its \"rowid\" column must hold integer row ids",
				domain.ErrFormulaViewNotPageable)
		}
		p.NextCursor = &v
	}
	return p, nil
}

// resolveFormulaParams validates the caller's values against the schema and returns the scalar parameter values keyed
// by name, the bulk (:rowids) row selection when the formula declares one, and (for a paginated view) its pagination
// plan. It rejects a missing required value, an unknown key, and a missing, empty or non-integer-array bulk selection.
// A parameter whose when-condition is false against the incoming values is not required — omitted, it binds like an
// omitted optional one; a supplied value still binds as usual. paginate enables the over-fetch-by-one on :limit
// (views only).
//
// exportRowCeiling, when positive, switches the pagination binds to export mode: :cursor binds 0 and :limit binds
// exportRowCeiling+1, bypassing the page-size clamps, so an export sweeps the full result in one pass and over-fetches
// exactly one row past the ceiling (a too-large result fails loud instead of truncating at a page boundary). Callers
// exporting pass paginate=false — export mode never trims pages, so no pagination plan applies. Zero keeps the
// pagination behavior unchanged.
func resolveFormulaParams(
	schema domain.ParamSchema, input FormulaRunInput, paginate bool, exportRowCeiling int64,
) (scalars map[string]domain.TypedValue, rowIDs []int64, plan paginationPlan, err error) {
	scalars = make(map[string]domain.TypedValue, len(schema))
	// The full schema is collected up front: a when-condition may reference a controller declared
	// after its gated parameter, and the closing unknown-key sweep reads the same map.
	byName := make(map[string]domain.FormulaParam, len(schema))
	for _, p := range schema {
		byName[p.Name] = p
	}

	for _, p := range schema {
		if p.Source == domain.ParamSourceSelectionBulk {
			raw, ok := input.Params[p.Name]
			if !ok {
				return nil, nil, paginationPlan{},
					paramValueErr(p.Name, "needs at least one selected row")
			}
			rowIDs, err = coerceRowIDs(p, raw)
			if err != nil {
				return nil, nil, paginationPlan{}, err
			}
			continue
		}
		if p.Source == domain.ParamSourcePagination {
			if exportRowCeiling > 0 {
				// A pagination param is either :cursor or :limit — bind the cursor's
				// start-of-sweep 0, overwritten with the over-fetched ceiling for :limit.
				scalars[p.Name] = domain.IntegerValue(0)
				if p.Name == domain.ReservedParamLimit {
					scalars[p.Name] = domain.IntegerValue(exportRowCeiling + 1)
				}
				continue
			}
			// Pagination controls are optional: missing :cursor starts from the beginning and
			// missing :limit falls back to the default page size (any :limit is clamped to the max).
			requested, perr := optionalInt64(p.Name, input.Params[p.Name], hasKey(input.Params, p.Name))
			if perr != nil {
				return nil, nil, paginationPlan{}, perr
			}
			scalars[p.Name] = resolvePaginationBind(p.Name, requested, paginate, &plan)
			continue
		}
		if p.Source == domain.ParamSourceWindow {
			// A window bound binds SQL NULL when omitted — an assistant run sees the view unfiltered,
			// and the SQL treats NULL as no bound — and a supplied day canonicalizes like any date.
			raw, ok := input.Params[p.Name]
			if !ok || raw == nil {
				scalars[p.Name] = domain.NullValue()
				continue
			}
			val, werr := coerceParamValue(p, raw)
			if werr != nil {
				return nil, nil, paginationPlan{}, werr
			}
			scalars[p.Name] = val
			continue
		}
		if p.Source == domain.ParamSourceReorder {
			raw, ok := input.Params[p.Name]
			if !ok || raw == nil {
				if p.Optional {
					// A withheld order binds SQL NULL: the renumbering statement's json_each yields no
					// rows, so positions stay untouched.
					scalars[p.Name] = domain.NullValue()
					continue
				}
				return nil, nil, paginationPlan{},
					paramValueErr(p.Name, "needs the full row order")
			}
			val, oerr := coerceOrderedRowIDs(p, raw)
			if oerr != nil {
				return nil, nil, paginationPlan{}, oerr
			}
			scalars[p.Name] = val
			continue
		}
		if p.Type == domain.ParamTypeGroup {
			val, gerr := coerceGroupValue(p, input.Params[p.Name], hasKey(input.Params, p.Name),
				paramConditionMet(p, byName, input.Params))
			if gerr != nil {
				return nil, nil, paginationPlan{}, gerr
			}
			scalars[p.Name] = val
			continue
		}
		if p.Multiple {
			val, merr := coerceMultiValue(p, input.Params[p.Name], hasKey(input.Params, p.Name),
				paramConditionMet(p, byName, input.Params))
			if merr != nil {
				return nil, nil, paginationPlan{}, merr
			}
			scalars[p.Name] = val
			continue
		}
		raw, ok := input.Params[p.Name]
		if !ok || raw == nil {
			// An optional parameter binds its declared default when omitted (or explicitly null), else SQL NULL.
			// A required one must carry a real value — a null filter comparison silently matches nothing — but
			// only while its when-condition holds: an inactive face binds like an omitted optional parameter.
			if p.Optional || !paramConditionMet(p, byName, input.Params) {
				if p.Default != "" {
					val, derr := bindDefault(p)
					if derr != nil {
						return nil, nil, paginationPlan{}, derr
					}
					scalars[p.Name] = val
					continue
				}
				scalars[p.Name] = domain.NullValue()
				continue
			}
			if !ok {
				return nil, nil, paginationPlan{},
					paramValueErr(p.Name, "needs a value")
			}
			return nil, nil, paginationPlan{},
				paramValueErr(p.Name, "is required and cannot be null")
		}
		val, cerr := coerceParamValue(p, raw)
		if cerr != nil {
			return nil, nil, paginationPlan{}, cerr
		}
		scalars[p.Name] = val
	}

	for name := range input.Params {
		if _, ok := byName[name]; !ok {
			return nil, nil, paginationPlan{},
				unknownParamErr(name, schema)
		}
	}

	return scalars, rowIDs, plan, nil
}

// paramConditionMet reports whether a parameter's when-condition holds against the run's incoming
// values, mirroring the app form's evaluation exactly: the controller's effective value is the
// supplied one (else its declared default, else empty), a boolean controller normalizes to
// "true"/"false", eq/ne compare exact strings, and the ordered ops compare numerically for integer
// and float controllers and as canonical strings for date and time ones — never holding while the
// controller is empty or unparseable. A parameter without a condition is always met, as is one
// whose stored condition names a controller the schema no longer declares.
func paramConditionMet(p domain.FormulaParam, byName map[string]domain.FormulaParam, params map[string]any) bool {
	w := p.When
	if w == nil {
		return true
	}
	controller, ok := byName[w.Param]
	if !ok {
		return true
	}
	raw := conditionControllerRaw(controller, params)
	switch op := w.EffectiveOp(); op {
	case domain.ParamWhenOpEq, domain.ParamWhenOpNe:
		actual := raw
		if controller.Type == domain.ParamTypeBoolean && raw != "true" {
			actual = "false"
		}
		if op == domain.ParamWhenOpEq {
			return actual == w.Value
		}
		return actual != w.Value
	default:
		if strings.TrimSpace(raw) == "" {
			return false
		}
		if controller.Type == domain.ParamTypeInteger || controller.Type == domain.ParamTypeFloat {
			a, aerr := strconv.ParseFloat(raw, 64)
			b, berr := strconv.ParseFloat(w.Value, 64)
			if aerr != nil || berr != nil {
				return false
			}
			return orderedHolds(op, cmp.Compare(a, b))
		}
		// Canonical date, datetime and time forms order correctly as plain strings.
		return orderedHolds(op, cmp.Compare(raw, w.Value))
	}
}

// conditionControllerRaw renders the controller's effective incoming value as the string the app's
// form field would hold: the supplied value when present (a datetime's "T" separator folding to the
// authored space form), else the controller's declared default, else empty.
func conditionControllerRaw(controller domain.FormulaParam, params map[string]any) string {
	raw, ok := params[controller.Name]
	if !ok || raw == nil {
		return controller.Default
	}
	switch v := raw.(type) {
	case string:
		if controller.Type == domain.ParamTypeDateTime {
			return strings.Replace(v, "T", " ", 1)
		}
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	default:
		// A mistyped value fails the controller's own coercion anyway; the condition reads it as empty.
		return ""
	}
}

// orderedHolds says whether an ordered when op (lt / lte / gt / gte) holds for a three-way
// comparison result — negative, zero or positive, the cmp.Compare convention. Any other op never
// holds.
func orderedHolds(op string, c int) bool {
	switch op {
	case domain.ParamWhenOpLt:
		return c < 0
	case domain.ParamWhenOpLte:
		return c <= 0
	case domain.ParamWhenOpGt:
		return c > 0
	case domain.ParamWhenOpGte:
		return c >= 0
	}
	return false
}

// bindDefault coerces a parameter's declared default — validated at create time to parse as the
// param's type — into the value an omitted caller binds.
// The parse errors are still checked: a stored schema predating stricter validation must fail the run loudly,
// not bind garbage.
func bindDefault(p domain.FormulaParam) (domain.TypedValue, error) {
	switch p.Type {
	case domain.ParamTypeInteger:
		n, err := strconv.ParseInt(p.Default, 10, 64)
		if err != nil {
			return domain.TypedValue{}, fmt.Errorf("%w: parameter %q default %q is not an integer",
				domain.ErrFormulaParamInvalid, p.Name, p.Default)
		}
		return domain.IntegerValue(n), nil
	case domain.ParamTypeFloat:
		f, err := strconv.ParseFloat(p.Default, 64)
		if err != nil {
			return domain.TypedValue{}, fmt.Errorf("%w: parameter %q default %q is not a number",
				domain.ErrFormulaParamInvalid, p.Name, p.Default)
		}
		return domain.RealValue(f), nil
	case domain.ParamTypeBoolean:
		switch p.Default {
		case "true":
			return domain.IntegerValue(1), nil
		case "false":
			return domain.IntegerValue(0), nil
		}
		return domain.TypedValue{}, fmt.Errorf("%w: parameter %q default %q is not \"true\" or \"false\"",
			domain.ErrFormulaParamInvalid, p.Name, p.Default)
	case domain.ParamTypeDate:
		canonical, ok := domain.CanonicalDate(p.Default)
		if !ok {
			return domain.TypedValue{}, fmt.Errorf("%w: parameter %q default %q is not a YYYY-MM-DD date",
				domain.ErrFormulaParamInvalid, p.Name, p.Default)
		}
		return domain.TextValue(canonical), nil
	case domain.ParamTypeDateTime:
		// Canonicalized, not passed through: a "T"-form default bound verbatim would plant in the column
		// the very mixed-shape ordering bug the type exists to prevent.
		canonical, ok := domain.CanonicalDateTime(p.Default)
		if !ok {
			return domain.TypedValue{}, fmt.Errorf("%w: parameter %q default %q is not a YYYY-MM-DD HH:MM[:SS] "+
				"date and time", domain.ErrFormulaParamInvalid, p.Name, p.Default)
		}
		return domain.TextValue(canonical), nil
	case domain.ParamTypeTime:
		canonical, ok := domain.CanonicalTime(p.Default)
		if !ok {
			return domain.TypedValue{}, fmt.Errorf("%w: parameter %q default %q is not a HH:MM[:SS] time",
				domain.ErrFormulaParamInvalid, p.Name, p.Default)
		}
		return domain.TextValue(canonical), nil
	default:
		return domain.TextValue(p.Default), nil
	}
}

// coerceRowIDs converts a JSON-decoded bulk-selection value into row IDs: a non-empty array of numbers.
func coerceRowIDs(p domain.FormulaParam, raw any) ([]int64, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, paramValueErr(p.Name, "expects an array of row IDs, got %T", raw)
	}
	if len(items) == 0 {
		return nil, paramValueErr(p.Name, "needs at least one selected row")
	}
	ids := make([]int64, len(items))
	for i, item := range items {
		f, ok := item.(float64) // JSON numbers decode to float64
		if !ok {
			return nil, paramValueErr(p.Name, "expects an array of row IDs, element %d is %T", i, item)
		}
		if f != math.Trunc(f) {
			return nil, paramValueErr(p.Name, "expects whole-number row IDs, element %d is %v", i, f)
		}
		ids[i] = int64(f)
	}
	return ids, nil
}

// coerceOrderedRowIDs converts a reorder value — a non-empty array of distinct whole-number
// rowids in their new display order — into the single JSON TEXT bind (e.g. "[5,3,8]") the SQL
// consumes with json_each, whose key column is each row's zero-based position. A duplicate is
// rejected: two entries for one row would make its renumbered position indeterminate.
func coerceOrderedRowIDs(p domain.FormulaParam, raw any) (domain.TypedValue, error) {
	items, ok := raw.([]any)
	if !ok {
		return domain.TypedValue{}, paramValueErr(p.Name, "expects an array of row IDs in their new order,"+
			" got %T", raw)
	}
	if len(items) == 0 {
		return domain.TypedValue{}, paramValueErr(p.Name, "needs at least one row")
	}
	ids := make([]int64, len(items))
	seen := make(map[int64]struct{}, len(items))
	for i, item := range items {
		f, ok := item.(float64) // JSON numbers decode to float64
		if !ok {
			return domain.TypedValue{}, paramValueErr(p.Name, "expects an array of row IDs, element %d is %T", i, item)
		}
		if f != math.Trunc(f) {
			return domain.TypedValue{}, paramValueErr(p.Name, "expects whole-number row IDs, element %d is %v", i, f)
		}
		id := int64(f)
		if _, dup := seen[id]; dup {
			return domain.TypedValue{}, paramValueErr(p.Name, "lists row ID %d twice — each row takes"+
				" one position", id)
		}
		seen[id] = struct{}{}
		ids[i] = id
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return domain.TypedValue{}, fmt.Errorf("marshaling reorder parameter %q: %w", p.Name, err)
	}
	return domain.TextValue(string(encoded)), nil
}

// hasKey reports whether m contains key, distinguishing an omitted value from an explicit JSON null.
func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

// coerceGroupValue converts a repeated-group value — a JSON-decoded array of objects — into the
// single JSON TEXT bind the SQL consumes with json_each. Each row is validated field-by-field
// like a scalar parameter and re-serialized normalized, so only declared fields travel (an
// optional field omitted from a row serializes as JSON null). An omitted or null optional group
// binds '[]', which json_each turns into zero rows; a required group needs at least one row —
// but only while its when-condition holds (conditionMet): an inactive face binds like an
// optional one.
func coerceGroupValue(p domain.FormulaParam, raw any, present, conditionMet bool) (domain.TypedValue, error) {
	if !present || raw == nil {
		if p.Optional || !conditionMet {
			return domain.TextValue("[]"), nil
		}
		if !present {
			return domain.TypedValue{}, paramValueErr(p.Name, "needs a value")
		}
		return domain.TypedValue{}, paramValueErr(p.Name, "is required and cannot be null")
	}
	items, ok := raw.([]any)
	if !ok {
		return domain.TypedValue{}, paramValueErr(p.Name, "expects an array of objects, got %T", raw)
	}
	if len(items) == 0 {
		if p.Optional || !conditionMet {
			return domain.TextValue("[]"), nil
		}
		return domain.TypedValue{}, paramValueErr(p.Name, "needs at least one row")
	}

	fieldsByName := make(map[string]domain.FormulaParam, len(p.Fields))
	for _, field := range p.Fields {
		fieldsByName[field.Name] = field
	}

	rows := make([]map[string]any, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return domain.TypedValue{}, paramValueErr(p.Name, "row %d expects an object, got %T", i, item)
		}
		for key := range obj {
			if _, declared := fieldsByName[key]; !declared {
				return domain.TypedValue{}, paramValueErr(p.Name, "row %d has unknown field %q", i, key)
			}
		}
		row := make(map[string]any, len(p.Fields))
		for _, field := range p.Fields {
			fraw, has := obj[field.Name]
			if !has || fraw == nil {
				if field.Optional {
					row[field.Name] = nil
					continue
				}
				if !has {
					return domain.TypedValue{}, paramValueErr(p.Name, "row %d is missing field %q", i, field.Name)
				}
				return domain.TypedValue{}, paramValueErr(p.Name, "row %d field %q is required and cannot be null", i,
					field.Name)
			}
			val, cerr := coerceParamValue(field, fraw)
			if cerr != nil {
				if inner, ok := errors.AsType[*domain.ParamValueError](cerr); ok {
					return domain.TypedValue{}, paramValueErr(p.Name, "row %d field %q %s", i, inner.Param,
						inner.Problem)
				}
				return domain.TypedValue{}, fmt.Errorf("parameter %q row %d: %w", p.Name, i, cerr)
			}
			// The normalized scalar (int64 / float64 / string; booleans already reduced to 0/1)
			// serializes back to JSON for the bind.
			row[field.Name] = val.Value
		}
		rows[i] = row
	}

	encoded, err := json.Marshal(rows)
	if err != nil {
		return domain.TypedValue{}, fmt.Errorf("marshaling group parameter %q: %w", p.Name, err)
	}
	return domain.TextValue(string(encoded)), nil
}

// coerceMultiValue converts a multi-select lookup value — a JSON-decoded array of scalars — into the single JSON
// TEXT bind (e.g. "[1,3]" or `["a","b"]`) the SQL consumes with json_each. Each element is coerced like a single
// pick of the parameter's type, array order is preserved, and a duplicate is rejected — one entry per picked
// value, or a member could be handed two shares. An omitted or null optional (or condition-false) parameter binds
// '[]', zero rows under json_each; a required one needs at least one element. An omitted "all" default is
// resolved into the incoming values before this point.
func coerceMultiValue(p domain.FormulaParam, raw any, present, conditionMet bool) (domain.TypedValue, error) {
	if !present || raw == nil {
		if p.Optional || !conditionMet {
			return domain.TextValue("[]"), nil
		}
		if !present {
			return domain.TypedValue{}, paramValueErr(p.Name, "needs a value")
		}
		return domain.TypedValue{}, paramValueErr(p.Name, "is required and cannot be null")
	}
	items, ok := raw.([]any)
	if !ok {
		return domain.TypedValue{}, paramValueErr(p.Name, "expects an array of values, got %T", raw)
	}
	if len(items) == 0 {
		if p.Optional || !conditionMet {
			return domain.TextValue("[]"), nil
		}
		return domain.TypedValue{}, paramValueErr(p.Name, "needs at least one value")
	}
	values := make([]any, len(items))
	for i, item := range items {
		val, cerr := coerceParamValue(p, item)
		if cerr != nil {
			if inner, ok := errors.AsType[*domain.ParamValueError](cerr); ok {
				return domain.TypedValue{}, paramValueErr(p.Name, "element %d %s", i, inner.Problem)
			}
			return domain.TypedValue{}, fmt.Errorf("parameter %q element %d: %w", p.Name, i, cerr)
		}
		values[i] = val.Value
	}
	if dup, found := firstDuplicate(values); found {
		return domain.TypedValue{}, paramValueErr(p.Name, "lists value %v twice", dup)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return domain.TypedValue{}, fmt.Errorf("marshaling multi-select parameter %q: %w", p.Name, err)
	}
	return domain.TextValue(string(encoded)), nil
}

// firstDuplicate returns the first value appearing more than once in values, in encounter order.
// The values are coerced scalars — int64, float64 or string.
func firstDuplicate(values []any) (any, bool) {
	seen := make(map[any]struct{}, len(values))
	for _, v := range values {
		if _, dup := seen[v]; dup {
			return v, true
		}
		seen[v] = struct{}{}
	}
	return nil, false
}

// maxResolvedAllOptions caps how many values a multi-select "all" default resolves to at bind time. A lookup
// table past this size would silently bind an outsized JSON array on every omitting run; failing loud surfaces
// the misfit instead — a set that large is a filter's job, not an enumerated pick.
const maxResolvedAllOptions = 1000

// resolveMultiDefaults fills each omitted (or explicitly null) multi-select parameter declaring a default,
// mutating params in place so the injected array travels the shared validation path exactly like a
// caller-supplied one: "all" resolves to every value the lookup table currently holds (or every option
// value on an options multi-select), and a declared subset resolves to its values shaped like a
// JSON-decoded caller array — numbers as float64, everything else as text.
func (s *FormulaService) resolveMultiDefaults(
	ctx context.Context, databaseID string, schema domain.ParamSchema, params map[string]any,
) error {
	for _, p := range schema {
		if !p.Multiple || (p.Default == "" && len(p.Defaults) == 0) {
			continue
		}
		if raw, ok := params[p.Name]; ok && raw != nil {
			continue
		}
		defaults := p.Defaults
		if p.Default == "all" {
			if p.Lookup != nil {
				values, ferr := s.fetchAllLookupValues(ctx, databaseID, p)
				if ferr != nil {
					return ferr
				}
				params[p.Name] = values
				continue
			}
			defaults = domain.OptionValues(p.Options)
		}
		values := make([]any, 0, len(defaults))
		for _, v := range defaults {
			values = append(values, defaultElementValue(p, v))
		}
		params[p.Name] = values
	}
	return nil
}

// defaultElementValue shapes one declared default element the way a JSON-decoded caller value arrives, so
// the shared element coercion applies unchanged: integer and float parameters carry a float64, every other
// type its text. A declared element always parses (validated at create time); a failing parse falls back to
// the text, which the coercion then rejects with its usual type error.
func defaultElementValue(p domain.FormulaParam, v string) any {
	switch p.Type {
	case domain.ParamTypeInteger, domain.ParamTypeFloat:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return v
}

// fetchAllLookupValues reads every distinct non-null value of the parameter's lookup value column, in value
// order, shaped like a JSON-decoded caller array (integers as float64) so the shared element coercion applies
// unchanged. The identifiers come from the stored lookup config; they are re-checked against the safe-name
// pattern here because they are interpolated, not bound.
func (s *FormulaService) fetchAllLookupValues(
	ctx context.Context, databaseID string, p domain.FormulaParam,
) ([]any, error) {
	for _, ident := range []string{p.Lookup.Table, p.Lookup.ValueColumn} {
		if !safeTableName.MatchString(ident) {
			return nil, fmt.Errorf("%w: parameter %q has an unsafe lookup identifier %q",
				domain.ErrFormulaParamInvalid, p.Name, ident)
		}
	}
	query := fmt.Sprintf(`SELECT DISTINCT "%s" FROM "%s" WHERE "%s" IS NOT NULL ORDER BY 1 LIMIT %d`,
		p.Lookup.ValueColumn, p.Lookup.Table, p.Lookup.ValueColumn, maxResolvedAllOptions+1)
	result, err := s.executor.Execute(ctx, databaseID, query, nil)
	if err != nil {
		return nil, fmt.Errorf("resolving parameter %q option values: %w", p.Name, err)
	}
	if len(result.Rows) > maxResolvedAllOptions {
		return nil, fmt.Errorf("%w: parameter %q resolves \"all\" to more than %d values",
			domain.ErrFormulaParamInvalid, p.Name, maxResolvedAllOptions)
	}
	values := make([]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) == 0 {
			continue
		}
		switch v := row[0].Value.(type) {
		case int64:
			values = append(values, float64(v))
		default:
			values = append(values, v)
		}
	}
	return values, nil
}

// coerceParamValue converts a JSON-decoded non-null value into a TypedValue of the parameter's
// declared type (null handling — NULL for optional params, rejection for required ones — happens
// before the value reaches this point).
func coerceParamValue(p domain.FormulaParam, raw any) (domain.TypedValue, error) {
	switch p.Type {
	case domain.ParamTypeText:
		s, ok := raw.(string)
		if !ok {
			return domain.TypedValue{}, typeErr(p, raw)
		}
		if len(p.Options) > 0 {
			canonical, matched := domain.MatchOption(p.Options, s)
			if !matched {
				return domain.TypedValue{}, optionErr(p, s)
			}
			return domain.TextValue(canonical), nil
		}
		return domain.TextValue(s), nil
	case domain.ParamTypeDate:
		s, ok := raw.(string)
		if !ok {
			return domain.TypedValue{}, typeErr(p, raw)
		}
		canonical, valid := domain.CanonicalDate(s)
		if !valid {
			return domain.TypedValue{}, paramValueErr(p.Name, "expects a YYYY-MM-DD date, got %q", s)
		}
		// A date param's option values are canonical by validation, so the canonical form matches exactly.
		if len(p.Options) > 0 {
			if _, matched := domain.MatchOption(p.Options, canonical); !matched {
				return domain.TypedValue{}, optionErr(p, s)
			}
		}
		return domain.TextValue(canonical), nil
	case domain.ParamTypeDateTime:
		s, ok := raw.(string)
		if !ok {
			return domain.TypedValue{}, typeErr(p, raw)
		}
		canonical, valid := domain.CanonicalDateTime(s)
		if !valid {
			return domain.TypedValue{}, paramValueErr(p.Name, "expects a YYYY-MM-DD HH:MM[:SS] date and time, "+
				"got %q", s)
		}
		return domain.TextValue(canonical), nil
	case domain.ParamTypeTime:
		s, ok := raw.(string)
		if !ok {
			return domain.TypedValue{}, typeErr(p, raw)
		}
		canonical, valid := domain.CanonicalTime(s)
		if !valid {
			return domain.TypedValue{}, paramValueErr(p.Name, "expects a HH:MM[:SS] time, got %q", s)
		}
		return domain.TextValue(canonical), nil
	case domain.ParamTypeInteger:
		f, ok := raw.(float64) // JSON numbers decode to float64
		if !ok {
			return domain.TypedValue{}, typeErr(p, raw)
		}
		if f != math.Trunc(f) {
			return domain.TypedValue{}, paramValueErr(p.Name, "expects a whole number, got %v", f)
		}
		if len(p.Options) > 0 && !domain.MatchOptionInt(p.Options, int64(f)) {
			return domain.TypedValue{}, optionErr(p, int64(f))
		}
		return domain.IntegerValue(int64(f)), nil
	case domain.ParamTypeFloat:
		f, ok := raw.(float64)
		if !ok {
			return domain.TypedValue{}, typeErr(p, raw)
		}
		if len(p.Options) > 0 && !domain.MatchOptionFloat(p.Options, f) {
			return domain.TypedValue{}, optionErr(p, f)
		}
		return domain.RealValue(f), nil
	case domain.ParamTypeBoolean:
		// Strictly a JSON boolean — 1/0 or "true" would be a silent coercion, so they are rejected.
		b, ok := raw.(bool)
		if !ok {
			return domain.TypedValue{}, typeErr(p, raw)
		}
		if b {
			return domain.IntegerValue(1), nil
		}
		return domain.IntegerValue(0), nil
	default:
		return domain.TypedValue{}, fmt.Errorf("%w: parameter %q has unknown type %q",
			domain.ErrFormulaParamInvalid, p.Name, p.Type)
	}
}

// typeErr rejects a value of the wrong JSON type for the parameter p.
func typeErr(p domain.FormulaParam, raw any) error {
	return paramValueErr(p.Name, "expects %s, got %T", p.Type, raw)
}

// optionErr rejects a value outside a parameter's closed set, listing the accepted values. A string
// value quotes like the list entries; numbers print bare.
func optionErr(p domain.FormulaParam, got any) error {
	if s, ok := got.(string); ok {
		got = strconv.Quote(s)
	}
	return paramValueErr(p.Name, "must be one of %s, got %v", quotedList(domain.OptionValues(p.Options)), got)
}

// paramValueErr returns a *domain.ParamValueError for the parameter name, its problem formatted from format and args.
func paramValueErr(name, format string, args ...any) error {
	return &domain.ParamValueError{Param: name, Problem: fmt.Sprintf(format, args...)}
}

// unknownParamErr returns the *domain.ParamValueError for name, which schema doesn't declare, listing the parameters
// it does, so a caller can correct the name without looking the formula up.
func unknownParamErr(name string, schema domain.ParamSchema) error {
	if len(schema) == 0 {
		return paramValueErr(name, "is unknown: the formula takes no parameters")
	}
	names := make([]string, len(schema))
	for i, p := range schema {
		names[i] = p.Name
	}
	return paramValueErr(name, "is unknown: the formula takes %s", strings.Join(names, ", "))
}

// quotedList renders accepted values for an error message as `"a", "b", "c"`.
func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return strings.Join(quoted, ", ")
}

// coerceParamValueFromString converts a query-string parameter value into the JSON-equivalent value
// coerceParamValue accepts: text passes through, integer and float parse via strconv, boolean is strictly
// "true" or "false". Needed by surfaces without JSON typing (URL query strings), so the shared resolution
// path stays the single validation point.
func coerceParamValueFromString(p domain.FormulaParam, raw string) (any, error) {
	// A group's rows travel as a JSON array of objects, the same shape a JSON caller sends.
	if p.Type == domain.ParamTypeGroup {
		var rows []any
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			return nil, paramValueErr(p.Name, "expects a JSON array of objects, got %q", raw)
		}
		return rows, nil
	}
	// A multi-select value, a bulk row selection and a reorder travel as a JSON array literal — the one
	// array shape a single key=value pair can carry; the shared element coercion applies from there.
	if p.Multiple || p.Source == domain.ParamSourceSelectionBulk || p.Source == domain.ParamSourceReorder {
		var items []any
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return nil, paramValueErr(p.Name, "expects a JSON array of values (e.g. [1,3]), got %q", raw)
		}
		return items, nil
	}
	switch p.Type {
	case domain.ParamTypeText, domain.ParamTypeDate, domain.ParamTypeDateTime, domain.ParamTypeTime:
		// The date and time shapes are checked where every other caller's are, once the value is typed.
		return raw, nil
	case domain.ParamTypeInteger:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, paramValueErr(p.Name, "expects an integer, got %q", raw)
		}
		// The shared path carries numbers as float64 (JSON's number type); reject the few int64 values a
		// float64 cannot hold exactly rather than silently rounding them.
		f := float64(n)
		if int64(f) != n {
			return nil, paramValueErr(p.Name, "value %q is too large", raw)
		}
		return f, nil
	case domain.ParamTypeFloat:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, paramValueErr(p.Name, "expects a number, got %q", raw)
		}
		return f, nil
	case domain.ParamTypeBoolean:
		switch raw {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, paramValueErr(p.Name, "expects \"true\" or \"false\", got %q", raw)
	default:
		return nil, fmt.Errorf("%w: parameter %q has unknown type %q",
			domain.ErrFormulaParamInvalid, p.Name, p.Type)
	}
}

// optionalInt64 reads an optional integer parameter value. present distinguishes an omitted value
// (nil result) from an explicit one; a present non-integer is an error.
func optionalInt64(name string, raw any, present bool) (*int64, error) {
	if !present {
		return nil, nil
	}
	f, ok := raw.(float64) // JSON numbers decode to float64
	if !ok {
		return nil, paramValueErr(name, "expects an integer, got %T", raw)
	}
	if f != math.Trunc(f) {
		return nil, paramValueErr(name, "expects a whole number, got %v", f)
	}
	n := int64(f)
	return &n, nil
}

// resolvePaginationBind turns a keyset-pagination control into the integer to bind.
// :cursor binds the requested rowid boundary, or 0 (before the first row) when omitted.
// :limit binds the defaulted + clamped page size; when paginate is set it binds one extra (the
// over-fetch used to detect a next page) and records the real page size in plan.
func resolvePaginationBind(name string, requested *int64, paginate bool, plan *paginationPlan) domain.TypedValue {
	if name != domain.ReservedParamLimit {
		var cursor int64
		if requested != nil {
			cursor = *requested
		}
		return domain.IntegerValue(cursor)
	}
	effLimit := domain.ResolvePageLimit(requested, domain.DefaultPageSize, domain.MaxPageSize)
	if !paginate {
		return domain.IntegerValue(effLimit)
	}
	plan.enabled = true
	plan.limit = effLimit
	return domain.IntegerValue(effLimit + 1) // over-fetch by one to detect a next page
}

// bindFormulaArgs prepares each of sql's statements to read its bind parameters in positional
// order, then builds one positional argument slice per statement: a named placeholder takes its
// scalar value (the same name may bind in several statements), an anonymous "?" (from the :rowids
// expansion) takes the next rowid in order. Returns nil args when no statement binds anything; the
// statement count is reported separately, since it is real either way.
func (s *FormulaService) bindFormulaArgs(
	ctx context.Context, databaseID, sql string, scalars map[string]domain.TypedValue, rowIDs []int64,
) ([][]domain.TypedValue, int, error) {
	var (
		namesPerStmt [][]string
		sqlErr       error
	)
	// The connection's own failures (opening the file, a cancelled request) pass through as they are: only sqlErr
	// describes the SQL.
	if err := s.executor.WithConn(ctx, databaseID, func(h ParseHandle) error {
		stmts, cerr := s.classifier.Classify(h, sql)
		if cerr != nil {
			sqlErr = cerr
			return nil
		}

		for _, cs := range stmts {
			res, perr := h.Parse(cs.SQL)
			if perr != nil {
				sqlErr = perr
				return nil
			}
			namesPerStmt = append(namesPerStmt, res.ParamNames)
		}
		return nil
	}); err != nil {
		return nil, 0, err
	}
	if sqlErr != nil {
		return nil, 0, fmt.Errorf("%w: %w", domain.ErrFormulaSQLInvalid, sqlErr)
	}

	argsPerStmt := make([][]domain.TypedValue, 0, len(namesPerStmt))
	nextRowID := 0
	hasBound := false

	for _, names := range namesPerStmt {
		stmtArgs := make([]domain.TypedValue, 0, len(names))
		for _, name := range names {
			if name == "" {
				if nextRowID >= len(rowIDs) {
					// The expansion produced exactly len(rowIDs) anonymous placeholders, so this is unreachable in
					// practice; guard rather than panic on a future mismatch.
					return nil, 0, fmt.Errorf("%w: row selection ran out of values", domain.ErrFormulaParamInvalid)
				}
				stmtArgs = append(stmtArgs, domain.IntegerValue(rowIDs[nextRowID]))
				nextRowID++
				continue
			}
			val, ok := scalars[name]
			if !ok {
				return nil, 0, fmt.Errorf("%w: SQL references parameter %q not in the schema",
					domain.ErrFormulaParamInvalid, name)
			}
			stmtArgs = append(stmtArgs, val)
		}

		if len(stmtArgs) > 0 {
			hasBound = true
		}

		argsPerStmt = append(argsPerStmt, stmtArgs)
	}

	if !hasBound {
		return nil, len(namesPerStmt), nil
	}
	return argsPerStmt, len(namesPerStmt), nil
}
