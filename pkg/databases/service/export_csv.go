package service

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// utf8BOM is prepended to CSV exports so spreadsheet tools (notably Excel) detect UTF-8 instead of falling
// back to a legacy locale encoding.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// ExportFormulaCSV runs a view formula and spools its full result set as a CSV artifact ready to stream.
// Requires DBPermRead — the CSV shows nothing a member couldn't already read through the view. The full
// result is always exported: caller-supplied pagination is rejected, a paginated view sweeps past the
// page-size clamps, and a result crossing the role-resolved row cap fails with ErrFormulaExportTooLarge
// rather than truncating. Rows stream straight into the spool file, so memory stays flat regardless of
// result size. Unlike a database export there is no rate limit and no in-flight guard — the work is one
// bounded query, not a snapshot copy.
func (s *ExportService) ExportFormulaCSV(
	ctx context.Context, userID, bdbID uuid.UUID, ref string, rawParams map[string]string,
) (*ExportArtifact, error) {
	db, err := s.dbSvc.AuthorizeAndResolveDatabase(ctx, userID, bdbID, authz.DBPermRead)
	if err != nil {
		return nil, err
	}
	databaseID := db.ID.String()
	now := time.Now()

	formula, err := s.formulaSvc.fetchFormulaByRef(ctx, databaseID, ref)
	if err != nil {
		return nil, err
	}
	if formula.Kind != domain.FormulaKindView {
		return nil, domain.ErrFormulaExportNotView
	}

	params, err := coerceExportParams(formula.Params, rawParams)
	if err != nil {
		return nil, err
	}
	if err := s.formulaSvc.resolveMultiDefaults(ctx, databaseID, formula.Params, params); err != nil {
		return nil, err
	}

	rowCap := int64(s.quotaResolver(authz.RolesFromContext(ctx)).MaxFormulaExportRows)
	if rowCap <= 0 {
		// A non-positive cap means unlimited; the ceiling still has to bind a concrete :limit, and the
		// export-mode over-fetch adds one — stay one below the int64 maximum so it cannot overflow.
		rowCap = math.MaxInt64 - 1
	}
	scalars, _, _, err := resolveFormulaParams(formula.Params, FormulaRunInput{Params: params}, false, rowCap)
	if err != nil {
		return nil, err
	}

	argsPerStmt, statementCount, err := s.formulaSvc.bindFormulaArgs(ctx, databaseID, formula.SQL, scalars, nil)
	if err != nil {
		return nil, err
	}
	// A view holds a single SELECT by construction; more than one statement means a corrupted store.
	if statementCount > 1 {
		return nil, fmt.Errorf("%w: a view must hold a single statement", domain.ErrFormulaSQLInvalid)
	}
	var args []domain.TypedValue
	if len(argsPerStmt) == 1 {
		args = argsPerStmt[0]
	}

	rec := domain.ExportRecord{
		ID:         uuid.New(),
		DatabaseID: db.ID,
		UserID:     userID,
		Kind:       domain.ExportKindFormulaCSV,
		Outcome:    domain.ExportOutcomeStarted,
		StartedAt:  now,
	}
	if err := s.repo.RecordStart(ctx, rec); err != nil {
		return nil, fmt.Errorf("recording export start: %w", err)
	}

	spoolPath := filepath.Join(s.stagingDir, fmt.Sprintf("%s-formula-%d.csv", databaseID, now.UnixNano()))
	size, err := s.spoolFormulaCSV(ctx, databaseID, formula.SQL, args, spoolPath, rowCap)
	if err != nil {
		s.recordOutcome(rec.ID, domain.ExportOutcomeFailed, 0)
		s.removeSpool(spoolPath)
		return nil, err
	}

	return &ExportArtifact{
		Path:      spoolPath,
		Filename:  fmt.Sprintf("%s-%s.csv", formula.Name, now.UTC().Format("20060102T150405Z")),
		SizeBytes: size,
		finish: func(streamErr error, bytesStreamed int64) {
			outcome := domain.ExportOutcomeSuccess
			if streamErr != nil {
				outcome = domain.ExportOutcomeFailed
			}
			s.recordOutcome(rec.ID, outcome, bytesStreamed)
			s.removeSpool(spoolPath)
		},
	}, nil
}

// coerceExportParams converts query-string parameter values into the run-input values the shared parameter
// resolution accepts. The pagination keys are rejected — an export always covers the full result — and a
// formula declaring a bulk row selection cannot be exported at all, since that selection only exists in
// the app's table UI.
func coerceExportParams(schema domain.ParamSchema, rawParams map[string]string) (map[string]any, error) {
	byName := make(map[string]domain.FormulaParam, len(schema))
	for _, p := range schema {
		if p.Source == domain.ParamSourceSelectionBulk {
			return nil, fmt.Errorf("%w: a formula selecting rows in bulk cannot be exported",
				domain.ErrFormulaParamInvalid)
		}
		byName[p.Name] = p
	}

	params := make(map[string]any, len(rawParams))
	for name, raw := range rawParams {
		if name == domain.ReservedParamCursor || name == domain.ReservedParamLimit {
			return nil, paramValueErr(name, "cannot be given: an export always returns the full result")
		}
		p, ok := byName[name]
		if !ok {
			return nil, unknownParamErr(name, schema)
		}
		val, err := coerceParamValueFromString(p, raw)
		if err != nil {
			return nil, err
		}
		params[name] = val
	}
	return params, nil
}

// spoolFormulaCSV streams the view's rows into a fresh CSV spool file at path — BOM, then a header row
// from the column names on the first scanned row, then one CSV record per row — and returns the file's
// final size. The scan is bounded by the service's query ceiling; crossing rowCap aborts the scan with
// ErrFormulaExportTooLarge.
func (s *ExportService) spoolFormulaCSV(
	ctx context.Context, databaseID, sql string, args []domain.TypedValue, path string, rowCap int64,
) (int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("creating export spool: %w", err)
	}

	queryCtx := ctx
	if s.queryTimeout > 0 {
		var cancel context.CancelFunc
		queryCtx, cancel = context.WithTimeout(ctx, s.queryTimeout)
		defer cancel()
	}

	spool := func() error {
		if _, werr := f.Write(utf8BOM); werr != nil {
			return fmt.Errorf("writing spool BOM: %w", werr)
		}
		w := csv.NewWriter(f)
		var rows int64
		serr := s.streamer.ExecuteStream(queryCtx, databaseID, sql, args,
			func(cols []domain.Column, row []domain.TypedValue) error {
				if rows == 0 {
					header := make([]string, len(cols))
					for i, c := range cols {
						header[i] = c.Name
					}
					if werr := w.Write(header); werr != nil {
						return werr
					}
				}
				rows++
				if rows > rowCap {
					return fmt.Errorf("%w: the view returned more than %d rows",
						domain.ErrFormulaExportTooLarge, rowCap)
				}
				fields := make([]string, len(row))
				for i, v := range row {
					fields[i] = csvField(v)
				}
				return w.Write(fields)
			})
		if serr != nil {
			// Attributes the failure to our ceiling only when it actually fired and the caller's own ctx
			// is still live — a caller abort must not masquerade as the export timeout.
			if errors.Is(queryCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				return domain.ErrFormulaExportTimeout
			}
			return serr
		}
		w.Flush()
		if werr := w.Error(); werr != nil {
			return fmt.Errorf("flushing export spool: %w", werr)
		}
		return nil
	}

	if err := spool(); err != nil {
		if cerr := f.Close(); cerr != nil {
			s.logger.Warn().Err(cerr).Str("path", path).Msg("export: closing spool file after failure")
		}
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("closing export spool: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stating export spool: %w", err)
	}
	return info.Size(), nil
}

// csvField renders one TypedValue as its CSV field text: NULL is an empty field, TEXT passes through guarded
// against spreadsheet formula evaluation (the csv writer applies RFC 4180 quoting), INTEGER and REAL format via
// strconv (floats in the shortest round-trippable form), and BLOB encodes as standard base64.
func csvField(v domain.TypedValue) string {
	switch val := v.Value.(type) {
	case nil:
		return ""
	case string:
		return guardSpreadsheetFormula(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'g', -1, 64)
	case []byte:
		return base64.StdEncoding.EncodeToString(val)
	default:
		return ""
	}
}

// guardSpreadsheetFormula prefixes a single quote to text that a spreadsheet would evaluate on open — a leading
// =, +, -, @, tab or carriage return — so a cell planted by one member of a bdb never runs as a formula on another
// member's machine. Only TEXT values are guarded; numeric columns render through strconv and keep their sign.
func guardSpreadsheetFormula(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
