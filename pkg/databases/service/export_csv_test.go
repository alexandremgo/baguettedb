package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// stubParseHandle reports canned bind-parameter names for any statement.
type stubParseHandle struct {
	paramNames []string
}

func (h stubParseHandle) Parse(string) (domain.ParseResult, error) {
	return domain.ParseResult{ParamNames: h.paramNames}, nil
}

// csvTestExecutor scripts fetchFormulaByRef's read via the embedded programmableExecutor and hands
// bindFormulaArgs a parse handle reporting the canned bind-parameter names.
type csvTestExecutor struct {
	programmableExecutor
	paramNames []string
}

func (e *csvTestExecutor) WithConn(_ context.Context, _ string, fn func(ParseHandle) error) error {
	return fn(stubParseHandle{paramNames: e.paramNames})
}

// stubClassifier classifies any SQL as a single statement.
type stubClassifier struct{}

func (stubClassifier) Classify(_ ParseHandle, sql string) ([]domain.ClassifiedStatement, error) {
	return []domain.ClassifiedStatement{{SQL: sql}}, nil
}

func (stubClassifier) ClassifyAlterTable(ParseHandle, string) (domain.AlterTableInfo, error) {
	return domain.AlterTableInfo{}, nil
}

// mockRowStreamer yields its scripted rows to every ExecuteStream callback, honoring callback aborts.
type mockRowStreamer struct {
	cols []domain.Column
	rows [][]domain.TypedValue
	err  error

	calls int
}

func (m *mockRowStreamer) ExecuteStream(
	_ context.Context, _ string, _ string, _ []domain.TypedValue,
	fn func(cols []domain.Column, row []domain.TypedValue) error,
) error {
	m.calls++
	if m.err != nil {
		return m.err
	}
	for _, r := range m.rows {
		if err := fn(m.cols, r); err != nil {
			return err
		}
	}
	return nil
}

// formulaRow builds the _formulas result row fetchFormulaByRef scans.
func formulaRow(
	t *testing.T, name, sqlText string, kind domain.FormulaKind, params domain.ParamSchema,
) []domain.TypedValue {
	t.Helper()
	paramsJSON, err := marshalParams(params)
	if err != nil {
		t.Fatalf("marshalParams: %v", err)
	}
	return []domain.TypedValue{
		domain.TextValue(uuid.NewString()),
		domain.TextValue(name),
		domain.TextValue("a test formula"),
		domain.TextValue(sqlText),
		domain.TextValue(string(kind)),
		domain.TextValue(paramsJSON),
		domain.NullValue(),
		domain.IntegerValue(0),
		domain.TextValue("2026-01-01 00:00:00"),
		domain.TextValue("2026-01-01 00:00:00"),
		domain.TextValue(""),
		domain.NullValue(),
		domain.IntegerValue(0),
		domain.NullValue(),
	}
}

// newCSVExportService builds an ExportService whose formula plumbing runs on scripted doubles: the given
// _formulas row is served to fetchFormulaByRef, rows come from the given streamer, and rowCap is the
// role-resolved export row limit.
func newCSVExportService(
	t *testing.T, repo *mockExportRepo, streamer rowStreamer, rowCap int, row []domain.TypedValue,
) (*ExportService, uuid.UUID) {
	t.Helper()

	db := &domain.Database{
		ID:                    uuid.New(),
		BdbID:                 uuid.New(),
		PlatformSchemaVersion: CurrentPlatformSchemaVersion,
	}
	dbSvc := NewDatabaseService(
		&mockDatabaseRepo{db: db}, &mockDatabaseManager{}, &mockQueryExecutor{}, &mockBdbsApp{},
		quota.Fixed(quota.Limits{}), newStubKEKProvider(t), zerolog.Nop())

	executor := &csvTestExecutor{}
	executor.matchFn = func(sql string) *domain.QueryResult {
		if strings.Contains(sql, domain.FormulasTable) {
			return &domain.QueryResult{Rows: [][]domain.TypedValue{row}}
		}
		return nil
	}
	formulaSvc := NewFormulaService(dbSvc, executor, stubClassifier{}, nil, 0, zerolog.Nop())

	svc, err := NewExportService(dbSvc, formulaSvc, &mockExportSnapshotter{}, streamer, repo,
		quota.Fixed(quota.Limits{MaxFormulaExportRows: rowCap}), t.TempDir(), 0, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewExportService: %v", err)
	}
	return svc, db.BdbID
}

func TestExportFormulaCSV_RejectsNonView(t *testing.T) {
	repo := &mockExportRepo{}
	streamer := &mockRowStreamer{}
	svc, bdbID := newCSVExportService(t, repo, streamer, 100,
		formulaRow(t, "cleanup", "DELETE FROM items", domain.FormulaKindAction, nil))

	_, err := svc.ExportFormulaCSV(context.Background(), uuid.New(), bdbID, "cleanup", nil)
	if !errors.Is(err, domain.ErrFormulaExportNotView) {
		t.Fatalf("err = %v, want ErrFormulaExportNotView", err)
	}
	if streamer.calls != 0 {
		t.Errorf("streamer calls = %d, want 0", streamer.calls)
	}
	if len(repo.started) != 0 {
		t.Errorf("audit rows started = %d, want 0 (rejected before any work)", len(repo.started))
	}
}

func TestExportFormulaCSV_RejectsCallerPagination(t *testing.T) {
	schema := domain.ParamSchema{
		{Name: domain.ReservedParamCursor, Type: domain.ParamTypeInteger, Source: domain.ParamSourcePagination},
		{Name: domain.ReservedParamLimit, Type: domain.ParamTypeInteger, Source: domain.ParamSourcePagination},
	}
	for _, key := range []string{domain.ReservedParamCursor, domain.ReservedParamLimit} {
		t.Run(key, func(t *testing.T) {
			repo := &mockExportRepo{}
			streamer := &mockRowStreamer{}
			svc, bdbID := newCSVExportService(t, repo, streamer, 100,
				formulaRow(t, "items",
					"SELECT rowid, name FROM items WHERE rowid > :cursor LIMIT :limit",
					domain.FormulaKindView, schema))

			_, err := svc.ExportFormulaCSV(context.Background(), uuid.New(), bdbID, "items",
				map[string]string{key: "10"})
			if !errors.Is(err, domain.ErrFormulaParamValue) {
				t.Fatalf("err = %v, want ErrFormulaParamValue", err)
			}
			if streamer.calls != 0 {
				t.Errorf("streamer calls = %d, want 0", streamer.calls)
			}
		})
	}
}

func TestExportFormulaCSV_RejectsBulkSelectionFormula(t *testing.T) {
	schema := domain.ParamSchema{
		{Name: domain.ReservedParamRowIDs, Type: domain.ParamTypeInteger,
			Source: domain.ParamSourceSelectionBulk},
	}
	repo := &mockExportRepo{}
	streamer := &mockRowStreamer{}
	svc, bdbID := newCSVExportService(t, repo, streamer, 100,
		formulaRow(t, "selected", "SELECT * FROM items WHERE rowid IN (:rowids)",
			domain.FormulaKindView, schema))

	_, err := svc.ExportFormulaCSV(context.Background(), uuid.New(), bdbID, "selected", nil)
	if !errors.Is(err, domain.ErrFormulaParamInvalid) {
		t.Fatalf("err = %v, want ErrFormulaParamInvalid", err)
	}
	if streamer.calls != 0 {
		t.Errorf("streamer calls = %d, want 0", streamer.calls)
	}
}

func TestExportFormulaCSV_RowCap(t *testing.T) {
	mkRows := func(n int) [][]domain.TypedValue {
		rows := make([][]domain.TypedValue, n)
		for i := range rows {
			rows[i] = []domain.TypedValue{domain.IntegerValue(int64(i))}
		}
		return rows
	}
	cols := []domain.Column{{Name: "x", DeclaredType: "INTEGER"}}
	view := func() []domain.TypedValue {
		return formulaRow(t, "items", "SELECT x FROM items", domain.FormulaKindView, nil)
	}

	t.Run("exactly at cap succeeds", func(t *testing.T) {
		repo := &mockExportRepo{}
		svc, bdbID := newCSVExportService(t, repo, &mockRowStreamer{cols: cols, rows: mkRows(3)}, 3, view())

		artifact, err := svc.ExportFormulaCSV(context.Background(), uuid.New(), bdbID, "items", nil)
		if err != nil {
			t.Fatalf("ExportFormulaCSV: %v", err)
		}
		if _, serr := os.Stat(artifact.Path); serr != nil {
			t.Fatalf("spool file: %v", serr)
		}
		if len(repo.started) != 1 || repo.started[0].Kind != domain.ExportKindFormulaCSV {
			t.Fatalf("audit start = %+v, want one formula_csv record", repo.started)
		}

		artifact.Finish(nil, artifact.SizeBytes)
		if repo.outcomes[repo.started[0].ID] != domain.ExportOutcomeSuccess {
			t.Errorf("outcome = %v, want success", repo.outcomes[repo.started[0].ID])
		}
		if _, serr := os.Stat(artifact.Path); !errors.Is(serr, os.ErrNotExist) {
			t.Errorf("spool should be removed after Finish, stat err = %v", serr)
		}
	})

	t.Run("one row past cap fails loud", func(t *testing.T) {
		repo := &mockExportRepo{}
		svc, bdbID := newCSVExportService(t, repo, &mockRowStreamer{cols: cols, rows: mkRows(4)}, 3, view())

		_, err := svc.ExportFormulaCSV(context.Background(), uuid.New(), bdbID, "items", nil)
		if !errors.Is(err, domain.ErrFormulaExportTooLarge) {
			t.Fatalf("err = %v, want ErrFormulaExportTooLarge", err)
		}
		if len(repo.started) != 1 {
			t.Fatalf("audit start rows = %d, want 1", len(repo.started))
		}
		if repo.outcomes[repo.started[0].ID] != domain.ExportOutcomeFailed {
			t.Errorf("outcome = %v, want failed", repo.outcomes[repo.started[0].ID])
		}
		entries, rerr := os.ReadDir(svc.stagingDir)
		if rerr != nil {
			t.Fatalf("reading staging dir: %v", rerr)
		}
		if len(entries) != 0 {
			t.Errorf("staging dir has %d leftover files, want 0", len(entries))
		}
	})
}

// TestExportFormulaCSV_RendersSpool verifies the artifact's actual bytes: the UTF-8 BOM, the header row
// from the column names, RFC 4180 round-tripping of embedded commas/quotes/newlines, NULL as an empty
// field, and BLOB as standard base64.
func TestExportFormulaCSV_RendersSpool(t *testing.T) {
	cols := []domain.Column{
		{Name: "name", DeclaredType: "TEXT"},
		{Name: "note", DeclaredType: "TEXT"},
		{Name: "data", DeclaredType: "BLOB"},
		{Name: "score", DeclaredType: "REAL"},
	}
	rows := [][]domain.TypedValue{
		{
			domain.TextValue("plain"),
			domain.TextValue("a,b \"quoted\"\nsecond line"),
			{Type: domain.SQLBlob, Value: []byte{0x00, 0x01, 0xFF}},
			domain.RealValue(3.14),
		},
		{
			domain.TextValue("empty-ish"),
			domain.NullValue(),
			domain.NullValue(),
			domain.IntegerValue(7),
		},
	}
	repo := &mockExportRepo{}
	svc, bdbID := newCSVExportService(t, repo, &mockRowStreamer{cols: cols, rows: rows}, 100,
		formulaRow(t, "notes", "SELECT name, note, data, score FROM notes", domain.FormulaKindView, nil))

	artifact, err := svc.ExportFormulaCSV(context.Background(), uuid.New(), bdbID, "notes", nil)
	if err != nil {
		t.Fatalf("ExportFormulaCSV: %v", err)
	}
	defer artifact.Finish(nil, artifact.SizeBytes)

	raw, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatalf("reading spool: %v", err)
	}
	if int64(len(raw)) != artifact.SizeBytes {
		t.Errorf("SizeBytes = %d, want %d (actual file size)", artifact.SizeBytes, len(raw))
	}
	bom := []byte{0xEF, 0xBB, 0xBF}
	if !bytes.HasPrefix(raw, bom) {
		t.Fatalf("spool does not start with the UTF-8 BOM, got % x", raw[:min(3, len(raw))])
	}

	records, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, bom))).ReadAll()
	if err != nil {
		t.Fatalf("parsing spool CSV: %v", err)
	}
	want := [][]string{
		{"name", "note", "data", "score"},
		{"plain", "a,b \"quoted\"\nsecond line", "AAH/", "3.14"},
		{"empty-ish", "", "", "7"},
	}
	if !reflect.DeepEqual(records, want) {
		t.Errorf("spool records = %q, want %q", records, want)
	}
}

// TestCSVField pins the locked TypedValue → CSV field mapping: NULL renders as an empty field, TEXT passes
// through untouched (the csv writer handles quoting), INTEGER renders base-10, REAL renders in the shortest
// round-trippable form, and BLOB renders as standard base64.
func TestCSVField(t *testing.T) {
	tests := []struct {
		name string
		v    domain.TypedValue
		want string
	}{
		{"null is empty", domain.NullValue(), ""},
		{"integer", domain.IntegerValue(42), "42"},
		{"negative integer", domain.IntegerValue(-7), "-7"},
		{"real", domain.RealValue(3.14), "3.14"},
		{"real with integral value", domain.RealValue(2), "2"},
		{"real in scientific range", domain.RealValue(1e21), "1e+21"},
		{"text passes through", domain.TextValue("hello"), "hello"},
		{"text keeps csv specials for the writer", domain.TextValue("a,b\"c\"\nd"), "a,b\"c\"\nd"},
		{"blob is standard base64", domain.TypedValue{Type: domain.SQLBlob, Value: []byte{0x00, 0x01, 0xFF}},
			"AAH/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := csvField(tt.v); got != tt.want {
				t.Errorf("csvField(%+v) = %q, want %q", tt.v, got, tt.want)
			}
		})
	}
}

// TestCSVField_GuardsSpreadsheetFormulas pins the formula-injection guard: TEXT starting with a character a
// spreadsheet evaluates gets a leading quote, other text and every numeric value render unchanged.
func TestCSVField_GuardsSpreadsheetFormulas(t *testing.T) {
	tests := []struct {
		name string
		in   domain.TypedValue
		want string
	}{
		{"equals", domain.TextValue("=1+1"), "'=1+1"},
		{"plus", domain.TextValue("+33 6 12 34 56 78"), "'+33 6 12 34 56 78"},
		{"minus", domain.TextValue("-5"), "'-5"},
		{"at", domain.TextValue("@SUM(A1)"), "'@SUM(A1)"},
		{"tab", domain.TextValue("\tx"), "'\tx"},
		{"carriage return", domain.TextValue("\rx"), "'\rx"},
		{"plain", domain.TextValue("plain"), "plain"},
		{"formula char inside", domain.TextValue("a=b"), "a=b"},
		{"empty", domain.TextValue(""), ""},
		{"negative integer", domain.IntegerValue(-5), "-5"},
		{"negative real", domain.RealValue(-2.5), "-2.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := csvField(tt.in); got != tt.want {
				t.Errorf("csvField(%v) = %q, want %q", tt.in.Value, got, tt.want)
			}
		})
	}
}
