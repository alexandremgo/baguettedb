package intraprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/databases/appconv"
	"github.com/alexandremgo/baguettedb/pkg/databases/appinput"
	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// DatabasesApp adapts the databases app's query, formula, export and import services, acting as one account, and
// converts their results into the cross-app transfer shapes or the command line's own.
type DatabasesApp struct {
	querySvc   *dbservice.QueryService
	formulaSvc *dbservice.FormulaService
	skillSvc   *dbservice.SkillService
	docsSvc    *dbservice.DocsService
	exportSvc  *dbservice.ExportService
	importSvc  *dbservice.ImportService
	account    Account
}

// NewDatabasesApp creates a DatabasesApp acting as account.
func NewDatabasesApp(
	querySvc *dbservice.QueryService, formulaSvc *dbservice.FormulaService, skillSvc *dbservice.SkillService,
	docsSvc *dbservice.DocsService, exportSvc *dbservice.ExportService, importSvc *dbservice.ImportService,
	account Account,
) *DatabasesApp {
	return &DatabasesApp{
		querySvc: querySvc, formulaSvc: formulaSvc, skillSvc: skillSvc, docsSvc: docsSvc, exportSvc: exportSvc,
		importSvc: importSvc, account: account,
	}
}

// ListTables returns the user tables of the bdb bdbID.
func (a *DatabasesApp) ListTables(ctx context.Context, bdbID uuid.UUID) ([]apptypes.TableInfo, error) {
	ctx = a.account.bind(ctx)
	infos, err := a.querySvc.ListTablesInfo(ctx, a.account.ID, bdbID)
	if err != nil {
		return nil, err
	}
	return appconv.TableInfos(infos), nil
}

// ListTableSchemas returns the user tables of the bdb bdbID with their columns and foreign keys.
func (a *DatabasesApp) ListTableSchemas(ctx context.Context, bdbID uuid.UUID) ([]apptypes.TableSchema, error) {
	ctx = a.account.bind(ctx)
	schemas, err := a.querySvc.ListTablesWithSchemas(ctx, a.account.ID, bdbID)
	if err != nil {
		return nil, err
	}
	return appconv.TableSchemas(schemas), nil
}

// ListFormulas returns every formula of the bdb bdbID, hidden ones included.
func (a *DatabasesApp) ListFormulas(ctx context.Context, bdbID uuid.UUID) ([]apptypes.Formula, error) {
	ctx = a.account.bind(ctx)
	formulas, err := a.formulaSvc.ListFormulasIncludingHidden(ctx, a.account.ID, bdbID)
	if err != nil {
		return nil, err
	}
	return appconv.Formulas(formulas), nil
}

// Execute runs sql in the bdb bdbID as the databases app's statement kind matching intent, and converts the result. A
// statement of another kind returns a *domain.StatementKindError, SQL with parameters a *domain.ParametersError.
func (a *DatabasesApp) Execute(
	ctx context.Context, bdbID uuid.UUID, sql string, intent domain.Intent,
) (domain.QueryResult, error) {
	ctx = a.account.bind(ctx)
	var dbIntent dbdomain.OperationIntent
	switch intent {
	case domain.IntentQuery:
		dbIntent = dbdomain.IntentQuery
	case domain.IntentData:
		dbIntent = dbdomain.IntentExecute
	case domain.IntentSchema:
		dbIntent = dbdomain.IntentSchema
	default:
		return domain.QueryResult{}, fmt.Errorf("unknown statement kind %d", intent)
	}

	r, err := a.querySvc.Execute(ctx, a.account.ID, bdbID, sql, nil, dbIntent)
	if err != nil {
		return domain.QueryResult{}, executeError(err)
	}

	return toQueryResult(r), nil
}

// executeError converts the databases app's statement-kind, missing-parameter and parameter value errors into the
// command line's own; any other error passes through.
func executeError(err error) error {
	if refused, ok := errors.AsType[*dbdomain.StatementKindError](err); ok {
		converted := &domain.StatementKindError{Statement: refused.Statement, Count: refused.Count}
		switch refused.Kind {
		case dbdomain.StatementDQL:
			converted.Kind = domain.IntentQuery
		case dbdomain.StatementDML:
			converted.Kind = domain.IntentData
		case dbdomain.StatementDDL:
			converted.Kind = domain.IntentSchema
		default:
			if refused.Count <= 1 {
				return err
			}
		}
		return converted
	}
	if missing, ok := errors.AsType[*dbdomain.MissingParametersError](err); ok {
		return &domain.ParametersError{Names: missing.Names}
	}
	if refused, ok := errors.AsType[*dbdomain.ParamValueError](err); ok {
		return &domain.ParamValueError{Param: refused.Param, Problem: refused.Problem}
	}
	return err
}

// toQueryResult converts the databases app's result of a statement, undo, redo or revert.
func toQueryResult(r *dbdomain.QueryResult) domain.QueryResult {
	result := domain.QueryResult{
		Columns:               make([]domain.Column, len(r.Columns)),
		Rows:                  make([][]any, len(r.Rows)),
		AffectedRowCount:      r.AffectedRowCount,
		LastInsertRowID:       r.LastInsertRowID,
		BatchID:               r.BatchID,
		AffectedRows:          make([]domain.AffectedRow, len(r.AffectedRows)),
		AffectedRowsTruncated: r.AffectedRowsTruncated,
	}
	for i, c := range r.Columns {
		result.Columns[i] = domain.Column{Name: c.Name, DeclaredType: c.DeclaredType}
	}
	for i, row := range r.Rows {
		values := make([]any, len(row))
		for j, v := range row {
			values[j] = v.Value
		}
		result.Rows[i] = values
	}
	for i, ar := range r.AffectedRows {
		result.AffectedRows[i] = domain.AffectedRow{
			TableName: ar.TableName, RowID: ar.RowID, Operation: ar.Operation,
			OldValues: ar.OldValues, NewValues: ar.NewValues,
		}
	}
	return result
}

// maxChangeEntries is the most change log entries the databases app returns in one listing.
const maxChangeEntries = 100

// ListChanges returns the bdb bdbID's changes matching filter, newest first, ignoring its Limit: the batches the most
// entries the databases app returns at once hold, the oldest marked Partial when the cap may have cut it.
func (a *DatabasesApp) ListChanges(
	ctx context.Context, bdbID uuid.UUID, filter domain.ChangeFilter,
) ([]domain.ChangeBatch, error) {
	ctx = a.account.bind(ctx)
	var kind dbdomain.ChangeKind
	switch filter.Kind {
	case "":
	case domain.ChangeKindData:
		kind = dbdomain.ChangeKindDML
	case domain.ChangeKindSchema:
		kind = dbdomain.ChangeKindDDL
	default:
		return nil, fmt.Errorf("unknown change kind %q", filter.Kind)
	}
	var byUser *uuid.UUID
	if filter.Mine {
		byUser = &a.account.ID
	}

	listed, err := a.querySvc.ListChanges(ctx, a.account.ID, bdbID, kind, filter.Table, byUser, maxChangeEntries)
	if err != nil {
		return nil, err
	}
	entries := 0
	batches := make([]domain.ChangeBatch, len(listed.Batches))
	for i, b := range listed.Batches {
		batches[i] = domain.ChangeBatch{
			ID: b.BatchID, Kind: changeKind(b.Kind), Origin: b.Origin,
			ChangedBy: b.ChangedBy, Mine: b.ChangedBy == a.account.ID.String(), ChangedAt: b.ChangedAt,
			Entries: make([]domain.ChangeEntry, len(b.Entries)),
		}
		for j, e := range b.Entries {
			entry := domain.ChangeEntry{ID: e.ID, Operation: e.Operation, Table: e.TableName, RowID: e.RowID,
				OldValues: e.OldValues, NewValues: e.NewValues}
			if e.Kind == dbdomain.ChangeKindDDL {
				entry.Table, entry.ObjectType, entry.Statement = e.ObjectName, e.ObjectType, e.DDLStatement
			}
			batches[i].Entries[j] = entry
		}
		entries += len(b.Entries)
	}
	if entries >= maxChangeEntries && len(batches) > 0 {
		batches[len(batches)-1].Partial = true
	}
	return batches, nil
}

// changeKind returns the command line's name for a change log kind.
func changeKind(kind dbdomain.ChangeKind) string {
	if kind == dbdomain.ChangeKindDDL {
		return domain.ChangeKindSchema
	}
	return domain.ChangeKindData
}

// Undo reverses the account's most recent change in the bdb bdbID, or anyone's when anyone is set.
func (a *DatabasesApp) Undo(ctx context.Context, bdbID uuid.UUID, anyone bool) (domain.QueryResult, error) {
	ctx = a.account.bind(ctx)
	undo := a.querySvc.UndoLastChange
	if anyone {
		undo = a.querySvc.UndoLastChangeAnyone
	}
	return reversal(undo(ctx, a.account.ID, bdbID))
}

// Redo re-applies the account's most recently undone change in the bdb bdbID, or anyone's when anyone is set.
func (a *DatabasesApp) Redo(ctx context.Context, bdbID uuid.UUID, anyone bool) (domain.QueryResult, error) {
	ctx = a.account.bind(ctx)
	redo := a.querySvc.RedoLastChange
	if anyone {
		redo = a.querySvc.RedoLastChangeAnyone
	}
	return reversal(redo(ctx, a.account.ID, bdbID))
}

// RevertBatch reverses every entry of the batch batchID in the bdb bdbID.
func (a *DatabasesApp) RevertBatch(ctx context.Context, bdbID uuid.UUID, batchID string) (domain.QueryResult, error) {
	ctx = a.account.bind(ctx)
	return reversal(a.querySvc.RevertChangeBatch(ctx, a.account.ID, bdbID, batchID))
}

// RevertEntry reverses the single change log entry entryID in the bdb bdbID.
func (a *DatabasesApp) RevertEntry(ctx context.Context, bdbID uuid.UUID, entryID int64) (domain.QueryResult, error) {
	ctx = a.account.bind(ctx)
	return reversal(a.querySvc.RevertChange(ctx, a.account.ID, bdbID, entryID))
}

// reversal converts the result of an undo, redo or revert, and its errors into the command line's own: empty stacks,
// an unknown change, and a conflict.
func reversal(r *dbdomain.QueryResult, err error) (domain.QueryResult, error) {
	if conflict, ok := errors.AsType[*dbservice.ConflictError](err); ok && conflict.Conflict != nil {
		converted := &domain.ChangeConflictError{
			Reason: conflict.Conflict.Reason, Hints: conflict.Conflict.ResolutionHints,
		}
		for _, b := range conflict.Conflict.BlockedBy {
			converted.BlockedBy = append(converted.BlockedBy, b.ID)
		}
		return domain.QueryResult{}, converted
	}
	switch {
	case errors.Is(err, dbdomain.ErrNothingToUndo):
		return domain.QueryResult{}, domain.ErrNothingToUndo
	case errors.Is(err, dbdomain.ErrNothingToRedo):
		return domain.QueryResult{}, domain.ErrNothingToRedo
	case errors.Is(err, dbdomain.ErrChangeLogEntryNotFound):
		return domain.QueryResult{}, domain.ErrChangeNotFound
	case err != nil:
		return domain.QueryResult{}, err
	}
	return toQueryResult(r), nil
}

// RunFormula runs the formula ref of the bdb bdbID with params, given as strings the databases app converts to each
// parameter's declared type.
func (a *DatabasesApp) RunFormula(
	ctx context.Context, bdbID uuid.UUID, ref string, params map[string]string,
) (domain.FormulaRun, error) {
	ctx = a.account.bind(ctx)
	r, err := a.formulaSvc.RunFormulaFromStrings(ctx, a.account.ID, bdbID, ref, params)
	if errors.Is(err, dbdomain.ErrFormulaNotFound) {
		return domain.FormulaRun{}, fmt.Errorf("%w named %q", domain.ErrFormulaNotFound, ref)
	}
	if err != nil {
		return domain.FormulaRun{}, executeError(err)
	}
	run := domain.FormulaRun{Kind: string(r.Kind), Result: toQueryResult(r.Result)}
	if r.Pagination != nil {
		run.HasMore, run.NextCursor = r.Pagination.HasMore, r.Pagination.NextCursor
	}
	return run, nil
}

// GetFormula returns the formula ref (a name or an ID) of the bdb bdbID with its parameters and SQL.
func (a *DatabasesApp) GetFormula(ctx context.Context, bdbID uuid.UUID, ref string) (domain.FormulaDetail, error) {
	ctx = a.account.bind(ctx)
	f, err := a.formulaSvc.GetFormula(ctx, a.account.ID, bdbID, ref)
	if errors.Is(err, dbdomain.ErrFormulaNotFound) {
		return domain.FormulaDetail{}, fmt.Errorf("%w named %q", domain.ErrFormulaNotFound, ref)
	}
	if err != nil {
		return domain.FormulaDetail{}, err
	}
	return toFormulaDetail(f), nil
}

// CreateFormula creates a formula in the bdb bdbID from spec, the JSON object the databases app's create-formula
// input takes, and returns it. A key the input does not know is an error, so a misspelled field is not dropped.
func (a *DatabasesApp) CreateFormula(ctx context.Context, bdbID uuid.UUID, spec []byte) (domain.FormulaDetail, error) {
	var req appinput.CreateFormula
	if err := decodeSpec(spec, &req); err != nil {
		return domain.FormulaDetail{}, err
	}
	input, err := req.Input()
	if err != nil {
		return domain.FormulaDetail{}, err
	}
	ctx = a.account.bind(ctx)
	f, err := a.formulaSvc.CreateFormula(ctx, a.account.ID, bdbID, input)
	if err != nil {
		return domain.FormulaDetail{}, refusedFormula(err)
	}
	return toFormulaDetail(f), nil
}

// UpdateFormula changes the formula ref (a name or an ID) of the bdb bdbID from spec, the JSON object of the fields to
// change the databases app's update-formula input takes, and returns it. A key the input does not know is an error.
func (a *DatabasesApp) UpdateFormula(
	ctx context.Context, bdbID uuid.UUID, ref string, spec []byte,
) (domain.FormulaDetail, error) {
	var req appinput.UpdateFormula
	if err := decodeSpec(spec, &req); err != nil {
		return domain.FormulaDetail{}, err
	}
	patch, err := req.Patch()
	if err != nil {
		return domain.FormulaDetail{}, err
	}
	ctx = a.account.bind(ctx)
	f, err := a.formulaSvc.UpdateFormula(ctx, a.account.ID, bdbID, ref, patch)
	if errors.Is(err, dbdomain.ErrFormulaNotFound) {
		return domain.FormulaDetail{}, fmt.Errorf("%w named %q", domain.ErrFormulaNotFound, ref)
	}
	if err != nil {
		return domain.FormulaDetail{}, refusedFormula(err)
	}
	return toFormulaDetail(f), nil
}

// DeleteFormula deletes the formula ref (a name or an ID) of the bdb bdbID.
func (a *DatabasesApp) DeleteFormula(ctx context.Context, bdbID uuid.UUID, ref string) error {
	ctx = a.account.bind(ctx)
	err := a.formulaSvc.DeleteFormula(ctx, a.account.ID, bdbID, ref)
	if errors.Is(err, dbdomain.ErrFormulaNotFound) {
		return fmt.Errorf("%w named %q", domain.ErrFormulaNotFound, ref)
	}
	return err
}

// ListSkills returns every skill of the bdb bdbID, with its body.
func (a *DatabasesApp) ListSkills(ctx context.Context, bdbID uuid.UUID) ([]apptypes.Skill, error) {
	ctx = a.account.bind(ctx)
	skills, err := a.skillSvc.ListSkills(ctx, a.account.ID, bdbID)
	if err != nil {
		return nil, err
	}
	return appconv.Skills(skills), nil
}

// GetSkill returns the skill ref (a name or an ID) of the bdb bdbID.
func (a *DatabasesApp) GetSkill(ctx context.Context, bdbID uuid.UUID, ref string) (apptypes.Skill, error) {
	ctx = a.account.bind(ctx)
	s, err := a.skillSvc.GetSkill(ctx, a.account.ID, bdbID, ref)
	if errors.Is(err, dbdomain.ErrSkillNotFound) {
		return apptypes.Skill{}, fmt.Errorf("%w named %q", domain.ErrSkillNotFound, ref)
	}
	if err != nil {
		return apptypes.Skill{}, err
	}
	return appconv.Skill(s), nil
}

// CreateSkill creates a skill in the bdb bdbID targeting tables, a bdb-level skill when tables is empty, and returns
// it.
func (a *DatabasesApp) CreateSkill(
	ctx context.Context, bdbID uuid.UUID, name, description, body string, tables []string,
) (apptypes.Skill, error) {
	ctx = a.account.bind(ctx)
	s, err := a.skillSvc.CreateSkill(ctx, a.account.ID, bdbID, dbservice.SkillInput{
		Name: name, Description: description, Body: body, TargetTables: tables,
	})
	if err != nil {
		return apptypes.Skill{}, err
	}
	return appconv.Skill(s), nil
}

// UpdateSkill applies change to the skill ref (a name or an ID) of the bdb bdbID and returns it.
func (a *DatabasesApp) UpdateSkill(
	ctx context.Context, bdbID uuid.UUID, ref string, change domain.SkillChange,
) (apptypes.Skill, error) {
	ctx = a.account.bind(ctx)
	s, err := a.skillSvc.UpdateSkill(ctx, a.account.ID, bdbID, ref, dbservice.SkillPatch{
		Name: change.Name, Description: change.Description, Body: change.Body, TargetTables: change.Tables,
	})
	if errors.Is(err, dbdomain.ErrSkillNotFound) {
		return apptypes.Skill{}, fmt.Errorf("%w named %q", domain.ErrSkillNotFound, ref)
	}
	if err != nil {
		return apptypes.Skill{}, err
	}
	return appconv.Skill(s), nil
}

// refusedFormula returns err as a *domain.FormulaRefusedError when the formulas manual explains how to fix it, as is
// otherwise.
func refusedFormula(err error) error {
	if dbservice.FormulasManualCovers(err) {
		return &domain.FormulaRefusedError{Err: err}
	}
	return err
}

// RenderManual renders the manual for topic, each operation named by its command, quoting the limits the account works
// under.
func (a *DatabasesApp) RenderManual(ctx context.Context, topic string) (string, error) {
	return a.docsSvc.Manual(a.account.bind(ctx), topic, domain.CommandForTool)
}

// decodeSpec decodes spec, a single JSON object, into v, refusing a key v does not declare and anything after the
// object.
func decodeSpec(spec []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(spec))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("reading the formula: %w", err)
	}
	if dec.More() {
		return errors.New("reading the formula: more than one JSON value")
	}
	return nil
}

// toFormulaDetail converts a formula of the databases app, with its parameters and SQL.
func toFormulaDetail(f *dbdomain.Formula) domain.FormulaDetail {
	return domain.FormulaDetail{
		Name:        f.Name,
		Label:       f.Label,
		Description: f.Description,
		Kind:        string(f.Kind),
		Verb:        string(f.StatementVerb),
		Hidden:      f.Hidden,
		SQL:         f.SQL,
		Params:      toFormulaParams(f.Params),
	}
}

// toFormulaParams converts the databases app's parameters of a formula, or of a group parameter's rows.
func toFormulaParams(params []dbdomain.FormulaParam) []domain.FormulaParam {
	out := make([]domain.FormulaParam, len(params))
	for i, p := range params {
		out[i] = domain.FormulaParam{
			Name:     p.Name,
			Type:     p.Type,
			Source:   string(p.Source),
			Required: !p.Optional,
			Default:  p.Default,
			Defaults: p.Defaults,
			Multiple: p.Multiple,
			Fields:   toFormulaParams(p.Fields),
		}
		for _, o := range p.Options {
			out[i].Options = append(out[i].Options, domain.FormulaOption{Value: o.Value, Label: o.Label})
		}
		if p.Lookup != nil {
			out[i].Lookup = &domain.FormulaLookup{
				Table: p.Lookup.Table, ValueColumn: p.Lookup.ValueColumn, LabelColumn: p.Lookup.LabelColumn,
				AllowNew: p.Lookup.AllowNew,
			}
		}
		if p.When != nil {
			out[i].When = &domain.FormulaParamWhen{Param: p.When.Param, Op: p.When.EffectiveOp(), Value: p.When.Value}
		}
	}
	return out
}

// ExportDatabase writes a copy of the bdb bdbID's database to w, encrypted under the one-time passphrase it returns.
func (a *DatabasesApp) ExportDatabase(ctx context.Context, bdbID uuid.UUID, w io.Writer) (domain.Export, error) {
	ctx = a.account.bind(ctx)
	artifact, err := a.exportSvc.ExportDatabase(ctx, a.account.ID, bdbID)
	if err != nil {
		return domain.Export{}, err
	}
	written, err := copyFile(w, artifact.Path)
	artifact.Finish(err, written)
	if err != nil {
		return domain.Export{}, err
	}
	return domain.Export{SizeBytes: written, Passphrase: artifact.Passphrase}, nil
}

// CheckImport reads the file src holds and checks that it is a bdb export the databases app can import, under
// passphrase when it is encrypted. An encrypted file without a passphrase is a domain.ErrPassphraseNeeded; one that
// doesn't open a domain.ErrImportUnreadable.
func (a *DatabasesApp) CheckImport(ctx context.Context, src io.Reader, passphrase string) error {
	return toImportError(a.importSvc.CheckImport(a.account.bind(ctx), src, passphrase))
}

// ImportDatabase installs the bdb export src holds, under passphrase when it is encrypted, as the database of the empty
// bdb bdbID. Its errors are CheckImport's, and the databases app's.
func (a *DatabasesApp) ImportDatabase(ctx context.Context, bdbID uuid.UUID, src io.Reader, passphrase string) error {
	ctx = a.account.bind(ctx)
	return toImportError(a.importSvc.ImportDatabase(ctx, a.account.ID, bdbID, src, passphrase))
}

// toImportError returns the command line's error for err, an import's, when it has one, err itself otherwise.
func toImportError(err error) error {
	switch {
	case errors.Is(err, dbdomain.ErrImportPassphraseNeeded):
		return domain.ErrPassphraseNeeded
	case errors.Is(err, dbdomain.ErrImportUnreadable):
		return domain.ErrImportUnreadable
	default:
		return err
	}
}

// copyFile copies the file at path to w and returns the bytes written.
func copyFile(w io.Writer, path string) (written int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("opening the export: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing the export: %w", cerr))
		}
	}()
	if written, err = io.Copy(w, f); err != nil {
		return written, fmt.Errorf("writing the export: %w", err)
	}
	return written, nil
}
