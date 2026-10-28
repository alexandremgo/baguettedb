//go:build integration

package integrationtest

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
)

// applyMediaDiary applies the embedded media-diary starter for a fresh user and returns the wired services
// with the new bdb's coordinates, ready for formula runs.
func applyMediaDiary(t *testing.T) (deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	userID = uuid.New()
	bdbID, err = svc.CreateFromStarter(t.Context(), userID, testRoles, "media-diary", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter(media-diary) error = %v", err)
	}
	return deps, userID, bdbID
}

// runFormula runs a formula by name through the real run path and fails the test on error.
func runFormula(
	t *testing.T, deps engineDeps, userID, bdbID uuid.UUID, name string, params map[string]any,
) *dbservice.FormulaRunResult {
	t.Helper()
	res, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, name, dbservice.FormulaRunInput{Params: params})
	if err != nil {
		t.Fatalf("RunFormula(%q) error = %v", name, err)
	}
	return res
}

// execSQL runs raw SQL against the bdb's database as the user and fails the test on error.
func execSQL(
	t *testing.T, deps engineDeps, userID, bdbID uuid.UUID, sql string, intent dbdomain.OperationIntent,
) *dbdomain.QueryResult {
	t.Helper()
	res, err := deps.querySvc.Execute(t.Context(), userID, bdbID, sql, nil, intent)
	if err != nil {
		t.Fatalf("Execute(%q) error = %v", sql, err)
	}
	return res
}

// cell returns the value at (row, named column), failing the test when the column is missing or the
// row index is out of range.
func cell(t *testing.T, res *dbdomain.QueryResult, row int, column string) any {
	t.Helper()
	for i, c := range res.Columns {
		if c.Name == column {
			if row >= len(res.Rows) {
				t.Fatalf("row %d out of range (%d rows)", row, len(res.Rows))
			}
			return res.Rows[row][i].Value
		}
	}
	t.Fatalf("column %q not in result (have %v)", column, res.Columns)
	return nil
}

// TestMediaDiaryShelfLoop drives the full shelf round trip through real formula runs: shelving shows an
// unchecked row, checking it logs one unrated diary entry while the row lingers checked, and un-checking
// removes exactly that entry and restores the unchecked row.
func TestMediaDiaryShelfLoop(t *testing.T) {
	deps, userID, bdbID := applyMediaDiary(t)

	runFormula(t, deps, userID, bdbID, "shelve-title", map[string]any{"title": "Dune", "kind": "book"})

	shelf := runFormula(t, deps, userID, bdbID, "shelf", nil)
	if len(shelf.Result.Rows) != 1 {
		t.Fatalf("shelf rows = %d, want 1 after shelving", len(shelf.Result.Rows))
	}
	if done := cell(t, shelf.Result, 0, "done"); done != int64(0) {
		t.Errorf("done = %v, want 0 (unchecked)", done)
	}
	rowID, ok := cell(t, shelf.Result, 0, "rowid").(int64)
	if !ok {
		t.Fatalf("rowid = %T, want int64", cell(t, shelf.Result, 0, "rowid"))
	}

	runFormula(t, deps, userID, bdbID, "mark-logged", map[string]any{"rowid": float64(rowID)})

	diary := runFormula(t, deps, userID, bdbID, "diary", nil)
	if len(diary.Result.Rows) != 1 {
		t.Fatalf("diary rows = %d, want 1 after checking", len(diary.Result.Rows))
	}
	entries := execSQL(t, deps, userID, bdbID, "SELECT rating, review FROM entries", dbdomain.IntentQuery)
	if len(entries.Rows) != 1 || entries.Rows[0][0].Value != nil || entries.Rows[0][1].Value != nil {
		t.Fatalf("entries = %+v, want one unrated, review-less entry", entries.Rows)
	}
	shelf = runFormula(t, deps, userID, bdbID, "shelf", nil)
	if len(shelf.Result.Rows) != 1 {
		t.Fatalf("shelf rows = %d, want the checked row lingering", len(shelf.Result.Rows))
	}
	if done := cell(t, shelf.Result, 0, "done"); done != int64(1) {
		t.Errorf("done = %v, want 1 (checked)", done)
	}

	runFormula(t, deps, userID, bdbID, "mark-logged", map[string]any{"rowid": float64(rowID)})

	diary = runFormula(t, deps, userID, bdbID, "diary", nil)
	if len(diary.Result.Rows) != 0 {
		t.Errorf("diary rows = %d, want 0 after un-checking", len(diary.Result.Rows))
	}
	shelf = runFormula(t, deps, userID, bdbID, "shelf", nil)
	if len(shelf.Result.Rows) != 1 {
		t.Fatalf("shelf rows = %d, want the row back", len(shelf.Result.Rows))
	}
	if done := cell(t, shelf.Result, 0, "done"); done != int64(0) {
		t.Errorf("done = %v, want 0 (back to unchecked)", done)
	}
}

// TestMediaDiaryUncheckKeepsRatedEntry pins the precision guarantee: once the auto-created entry has been
// rated from the diary, un-checking the shelf row gives back the slot but must not discard the entry.
func TestMediaDiaryUncheckKeepsRatedEntry(t *testing.T) {
	deps, userID, bdbID := applyMediaDiary(t)

	runFormula(t, deps, userID, bdbID, "shelve-title", map[string]any{"title": "Heat"})
	shelf := runFormula(t, deps, userID, bdbID, "shelf", nil)
	rowID := cell(t, shelf.Result, 0, "rowid").(int64)
	runFormula(t, deps, userID, bdbID, "mark-logged", map[string]any{"rowid": float64(rowID)})

	entryID, ok := cell(t, execSQL(t, deps, userID, bdbID, "SELECT id FROM entries", dbdomain.IntentQuery),
		0, "id").(int64)
	if !ok {
		t.Fatal("no entry created by the check")
	}
	runFormula(t, deps, userID, bdbID, "edit-entry", map[string]any{"rowid": float64(entryID), "rating": 4.5})

	runFormula(t, deps, userID, bdbID, "mark-logged", map[string]any{"rowid": float64(rowID)})

	entries := execSQL(t, deps, userID, bdbID, "SELECT rating FROM entries", dbdomain.IntentQuery)
	if len(entries.Rows) != 1 {
		t.Fatalf("entries = %d, want the rated entry to survive the un-check", len(entries.Rows))
	}
	if rating := entries.Rows[0][0].Value; rating != int64(9) {
		t.Errorf("rating = %v, want 9 (4.5 stars doubled)", rating)
	}
	shelf = runFormula(t, deps, userID, bdbID, "shelf", nil)
	if len(shelf.Result.Rows) != 1 || cell(t, shelf.Result, 0, "done") != int64(0) {
		t.Errorf("shelf = %+v, want the row back unchecked", shelf.Result.Rows)
	}
}

// TestMediaDiaryShelfLinger pins the 10-minute linger boundary: a checked row stays on the shelf just
// inside the window and drops out just past it, without touching the title or its diary entry.
func TestMediaDiaryShelfLinger(t *testing.T) {
	deps, userID, bdbID := applyMediaDiary(t)

	runFormula(t, deps, userID, bdbID, "shelve-title", map[string]any{"title": "Ran"})
	shelf := runFormula(t, deps, userID, bdbID, "shelf", nil)
	rowID := cell(t, shelf.Result, 0, "rowid").(int64)
	runFormula(t, deps, userID, bdbID, "mark-logged", map[string]any{"rowid": float64(rowID)})

	execSQL(t, deps, userID, bdbID,
		"UPDATE titles SET checked_at = datetime('now', '-9 minutes')", dbdomain.IntentExecute)
	if shelf = runFormula(t, deps, userID, bdbID, "shelf", nil); len(shelf.Result.Rows) != 1 {
		t.Errorf("shelf rows = %d, want the row still lingering at 9 minutes", len(shelf.Result.Rows))
	}

	execSQL(t, deps, userID, bdbID,
		"UPDATE titles SET checked_at = datetime('now', '-11 minutes')", dbdomain.IntentExecute)
	if shelf = runFormula(t, deps, userID, bdbID, "shelf", nil); len(shelf.Result.Rows) != 0 {
		t.Errorf("shelf rows = %d, want the row gone past 10 minutes", len(shelf.Result.Rows))
	}

	titles := execSQL(t, deps, userID, bdbID, "SELECT COUNT(*) AS n FROM titles", dbdomain.IntentQuery)
	entries := execSQL(t, deps, userID, bdbID, "SELECT COUNT(*) AS n FROM entries", dbdomain.IntentQuery)
	if cell(t, titles, 0, "n") != int64(1) || cell(t, entries, 0, "n") != int64(1) {
		t.Errorf("titles/entries = %v/%v, want 1/1 — the linger drop is display-only",
			cell(t, titles, 0, "n"), cell(t, entries, 0, "n"))
	}
}

// TestMediaDiaryLogTitleInputs feeds log-title the awkward inputs an assistant or a hurried user actually
// sends and pins how each lands: case-folded enum spellings bind the declared option, unknown ones fail
// with the allowed list, out-of-range ratings clamp into the advertised 0.5-5, and an unparseable date
// falls back to today.
func TestMediaDiaryLogTitleInputs(t *testing.T) {
	deps, userID, bdbID := applyMediaDiary(t)

	t.Run("kind case-folds to the declared option", func(t *testing.T) {
		runFormula(t, deps, userID, bdbID, "log-title", map[string]any{"title": "Alien", "kind": "Film"})
		res := execSQL(t, deps, userID, bdbID,
			"SELECT kind FROM titles WHERE title = 'Alien'", dbdomain.IntentQuery)
		if kind := cell(t, res, 0, "kind"); kind != "film" {
			t.Errorf("kind = %v, want the declared spelling %q", kind, "film")
		}
	})

	t.Run("unknown kind errors with the allowed list", func(t *testing.T) {
		_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "log-title",
			dbservice.FormulaRunInput{Params: map[string]any{"title": "Solaris", "kind": "movie"}})
		if !errors.Is(err, dbdomain.ErrFormulaParamValue) {
			t.Fatalf("error = %v, want ErrFormulaParamValue", err)
		}
		for _, want := range []string{"must be one of", "film", "tv", "book"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("rating clamps into 0.5-5", func(t *testing.T) {
		runFormula(t, deps, userID, bdbID, "log-title", map[string]any{"title": "Ikiru", "rating": 6.0})
		runFormula(t, deps, userID, bdbID, "log-title", map[string]any{"title": "Yojimbo", "rating": 0.0})
		res := execSQL(t, deps, userID, bdbID,
			"SELECT t.title, e.rating FROM entries e JOIN titles t ON t.id = e.title_id ORDER BY t.title",
			dbdomain.IntentQuery)
		byTitle := map[string]any{}
		for i := range res.Rows {
			byTitle[cell(t, res, i, "title").(string)] = cell(t, res, i, "rating")
		}
		if byTitle["Ikiru"] != int64(10) {
			t.Errorf("Ikiru rating = %v, want 10 (6 clamped to 5 stars, doubled)", byTitle["Ikiru"])
		}
		if byTitle["Yojimbo"] != int64(1) {
			t.Errorf("Yojimbo rating = %v, want 1 (0 clamped to 0.5 stars, doubled)", byTitle["Yojimbo"])
		}
	})

	t.Run("unparseable date is rejected up front", func(t *testing.T) {
		_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "log-title",
			dbservice.FormulaRunInput{Params: map[string]any{"title": "Stalker", "logged_on": "yesterday"}})
		if !errors.Is(err, dbdomain.ErrFormulaParamValue) {
			t.Fatalf("error = %v, want ErrFormulaParamValue — the date param validates before the SQL runs", err)
		}
		res := execSQL(t, deps, userID, bdbID,
			"SELECT COUNT(*) AS n FROM titles WHERE title = 'Stalker'", dbdomain.IntentQuery)
		if n := cell(t, res, 0, "n"); n != int64(0) {
			t.Errorf("titles written by the rejected run = %v, want none", n)
		}
	})

	t.Run("omitted date logs today, supplied date binds as given", func(t *testing.T) {
		runFormula(t, deps, userID, bdbID, "log-title", map[string]any{"title": "Ran"})
		runFormula(t, deps, userID, bdbID, "log-title",
			map[string]any{"title": "Kagemusha", "logged_on": "2026-08-01"})
		res := execSQL(t, deps, userID, bdbID,
			"SELECT t.title, e.logged_on, (e.logged_on = date('now')) AS is_today FROM entries e "+
				"JOIN titles t ON t.id = e.title_id WHERE t.title IN ('Ran', 'Kagemusha') ORDER BY t.title",
			dbdomain.IntentQuery)
		if today := cell(t, res, 1, "is_today"); today != int64(1) {
			t.Errorf("Ran is_today = %v, want 1 — omission falls back to date('now')", today)
		}
		if on := cell(t, res, 0, "logged_on"); on != "2026-08-01" {
			t.Errorf("Kagemusha logged_on = %v, want the supplied date", on)
		}
	})
}

// TestMediaDiaryKindOptionsLabeled pins the labeled kind vocabulary on every site: all five kind
// params read back Film / TV / Book labels over the unchanged lowercase values, and the edit's
// omitted kind keeps the stored one now that the empty-string guard is gone from the SQL.
func TestMediaDiaryKindOptionsLabeled(t *testing.T) {
	deps, userID, bdbID := applyMediaDiary(t)

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	wantSites := map[string]bool{
		"diary": false, "log-title": false, "edit-title": false, "shelve-title": false, "edit-shelf-title": false,
	}
	want := []dbdomain.FormulaOption{{Value: "film", Label: "Film"}, {Value: "tv", Label: "TV"},
		{Value: "book", Label: "Book"}}
	for _, f := range formulas {
		if _, ok := wantSites[f.Name]; !ok {
			continue
		}
		for _, p := range f.Params {
			if p.Name != "kind" {
				continue
			}
			wantSites[f.Name] = true
			if len(p.Options) != len(want) {
				t.Fatalf("%s kind options = %v, want %v", f.Name, p.Options, want)
			}
			for i, opt := range p.Options {
				if opt != want[i] {
					t.Errorf("%s kind option %d = %+v, want %+v", f.Name, i, opt, want[i])
				}
			}
		}
	}
	for name, seen := range wantSites {
		if !seen {
			t.Errorf("%s: kind param with options not found", name)
		}
	}

	runFormula(t, deps, userID, bdbID, "shelve-title", map[string]any{"title": "Dune", "kind": "book"})
	rowid := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM titles WHERE title = 'Dune'", dbdomain.IntentQuery), 0, "id")
	runFormula(t, deps, userID, bdbID, "edit-shelf-title",
		map[string]any{"rowid": float64(rowid.(int64)), "title": "Dune Messiah"})
	res := execSQL(t, deps, userID, bdbID,
		"SELECT title, kind FROM titles", dbdomain.IntentQuery)
	if title, kind := cell(t, res, 0, "title"), cell(t, res, 0, "kind"); title != "Dune Messiah" || kind != "book" {
		t.Errorf("after kind-less edit: title/kind = %v/%v, want the new title with the kept kind", title, kind)
	}
}

// TestMediaDiaryEditEntryKeepsDateOnOmission pins edit-entry's omit-keeps-current date contract
// through the typed date param.
func TestMediaDiaryEditEntryKeepsDateOnOmission(t *testing.T) {
	deps, userID, bdbID := applyMediaDiary(t)

	runFormula(t, deps, userID, bdbID, "log-title",
		map[string]any{"title": "Solaris", "logged_on": "2026-07-15"})
	entryID := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM entries", dbdomain.IntentQuery), 0, "id")
	runFormula(t, deps, userID, bdbID, "edit-entry",
		map[string]any{"rowid": float64(entryID.(int64)), "rating": 4.5})
	res := execSQL(t, deps, userID, bdbID,
		"SELECT logged_on, rating FROM entries", dbdomain.IntentQuery)
	if on := cell(t, res, 0, "logged_on"); on != "2026-07-15" {
		t.Errorf("logged_on after a date-less edit = %v, want the stored date kept", on)
	}
	if rating := cell(t, res, 0, "rating"); rating != int64(9) {
		t.Errorf("rating = %v, want 9 (4.5 doubled)", rating)
	}
}

// TestMediaDiaryShelfAndListsFilters pins the two new filter panels: the shelf narrows by search
// (title or creator) and by kind, the lists view by search over name or description, and an omitted
// filter means everything rather than nothing.
func TestMediaDiaryShelfAndListsFilters(t *testing.T) {
	deps, userID, bdbID := applyMediaDiary(t)

	runFormula(t, deps, userID, bdbID, "shelve-title",
		map[string]any{"title": "Dune", "kind": "book", "creator": "Frank Herbert"})
	runFormula(t, deps, userID, bdbID, "shelve-title", map[string]any{"title": "Heat", "kind": "film"})

	if all := runFormula(t, deps, userID, bdbID, "shelf", nil); len(all.Result.Rows) != 2 {
		t.Fatalf("unfiltered shelf rows = %d, want 2", len(all.Result.Rows))
	}
	if books := runFormula(t, deps, userID, bdbID, "shelf",
		map[string]any{"kind": "book"}); len(books.Result.Rows) != 1 {
		t.Errorf("kind=book shelf rows = %d, want only Dune", len(books.Result.Rows))
	}
	byCreator := runFormula(t, deps, userID, bdbID, "shelf", map[string]any{"search": "herbert"})
	if len(byCreator.Result.Rows) != 1 || cell(t, byCreator.Result, 0, "title") != "Dune" {
		t.Errorf("search=herbert shelf rows = %v, want only Dune via its creator", byCreator.Result.Rows)
	}

	runFormula(t, deps, userID, bdbID, "create-list",
		map[string]any{"name": "Best of 2026", "description": "the year's keepers"})
	runFormula(t, deps, userID, bdbID, "create-list", map[string]any{"name": "Comfort rewatches"})

	if all := runFormula(t, deps, userID, bdbID, "lists", nil); len(all.Result.Rows) != 2 {
		t.Fatalf("unfiltered lists rows = %d, want 2", len(all.Result.Rows))
	}
	if byName := runFormula(t, deps, userID, bdbID, "lists",
		map[string]any{"search": "comfort"}); len(byName.Result.Rows) != 1 {
		t.Errorf("search=comfort lists rows = %d, want only Comfort rewatches", len(byName.Result.Rows))
	}
	byDesc := runFormula(t, deps, userID, bdbID, "lists", map[string]any{"search": "keepers"})
	if len(byDesc.Result.Rows) != 1 || cell(t, byDesc.Result, 0, "name") != "Best of 2026" {
		t.Errorf("search=keepers lists rows = %v, want only Best of 2026 via its description", byDesc.Result.Rows)
	}
}

// TestMediaDiaryListItemActions drives the list-item surface: the card body carries where-you-stand
// state, title-detail reads through the membership row, log-from-list writes a clamped rated entry
// and takes the title off the shelf without arming the checkbox, and shelve-from-list shelves a
// not-shelved item while leaving an actively shelved one untouched.
func TestMediaDiaryListItemActions(t *testing.T) {
	deps, userID, bdbID := applyMediaDiary(t)

	runFormula(t, deps, userID, bdbID, "shelve-title",
		map[string]any{"title": "Dune", "kind": "book", "creator": "Frank Herbert"})
	runFormula(t, deps, userID, bdbID, "create-list", map[string]any{"name": "Sci-fi canon"})
	listID := cell(t, execSQL(t, deps, userID, bdbID, "SELECT id FROM lists", dbdomain.IntentQuery), 0, "id")
	titleID := cell(t, execSQL(t, deps, userID, bdbID, "SELECT id FROM titles", dbdomain.IntentQuery), 0, "id")
	runFormula(t, deps, userID, bdbID, "add-to-list",
		map[string]any{"parent_rowid": float64(listID.(int64)), "title_id": float64(titleID.(int64))})

	items := runFormula(t, deps, userID, bdbID, "list-detail", map[string]any{"rowid": float64(listID.(int64))})
	if len(items.Result.Rows) != 1 {
		t.Fatalf("list items = %d, want 1", len(items.Result.Rows))
	}
	itemID := cell(t, items.Result, 0, "rowid").(int64)
	body, _ := cell(t, items.Result, 0, "body").(string)
	if !strings.Contains(body, "on the shelf") {
		t.Errorf("unlogged shelved item body = %q, want the shelf state", body)
	}

	detail := runFormula(t, deps, userID, bdbID, "title-detail", map[string]any{"rowid": float64(itemID)})
	if v := cell(t, detail.Result, 0, "shelf"); v != "on the shelf" {
		t.Errorf("title-detail shelf = %v, want on the shelf", v)
	}
	if v := cell(t, detail.Result, 0, "times_logged"); v != int64(0) {
		t.Errorf("title-detail times_logged = %v, want 0", v)
	}

	runFormula(t, deps, userID, bdbID, "log-from-list",
		map[string]any{"rowid": float64(itemID), "rating": 4.5, "logged_on": "2026-08-10"})
	entries := execSQL(t, deps, userID, bdbID,
		"SELECT rating, logged_on, is_repeat FROM entries", dbdomain.IntentQuery)
	if len(entries.Rows) != 1 || cell(t, entries, 0, "rating") != int64(9) ||
		cell(t, entries, 0, "logged_on") != "2026-08-10" || cell(t, entries, 0, "is_repeat") != int64(0) {
		t.Fatalf("entries after log-from-list = %+v, want one first-time 9-rated entry on the date", entries.Rows)
	}
	shelfState := execSQL(t, deps, userID, bdbID,
		"SELECT (checked_at IS NOT NULL) AS checked, (checked_entry_id IS NULL) AS unarmed FROM titles",
		dbdomain.IntentQuery)
	if cell(t, shelfState, 0, "checked") != int64(1) || cell(t, shelfState, 0, "unarmed") != int64(1) {
		t.Errorf("title after log-from-list = %+v, want checked off the shelf with the checkbox unarmed",
			shelfState.Rows)
	}

	items = runFormula(t, deps, userID, bdbID, "list-detail", map[string]any{"rowid": float64(listID.(int64))})
	body, _ = cell(t, items.Result, 0, "body").(string)
	if !strings.Contains(body, "★★★★½") || !strings.Contains(body, "read once") {
		t.Errorf("logged item body = %q, want the last rating and read-once state", body)
	}

	// The title was checked off above, so shelving it from the list re-shelves it fresh; a second
	// shelve is the active-shelf no-op.
	runFormula(t, deps, userID, bdbID, "shelve-from-list", map[string]any{"rowid": float64(itemID)})
	first := execSQL(t, deps, userID, bdbID,
		"SELECT shelved_at, (checked_at IS NULL) AS unchecked FROM titles", dbdomain.IntentQuery)
	if cell(t, first, 0, "unchecked") != int64(1) {
		t.Fatalf("title after shelve-from-list = %+v, want back on the shelf unchecked", first.Rows)
	}
	shelvedAt := cell(t, first, 0, "shelved_at")
	res := runFormula(t, deps, userID, bdbID, "shelve-from-list", map[string]any{"rowid": float64(itemID)})
	if res.Result.AffectedRowCount != 0 {
		t.Errorf("second shelve-from-list affected %d rows, want the no-op", res.Result.AffectedRowCount)
	}
	second := execSQL(t, deps, userID, bdbID, "SELECT shelved_at FROM titles", dbdomain.IntentQuery)
	if cell(t, second, 0, "shelved_at") != shelvedAt {
		t.Errorf("shelved_at bumped by the no-op: %v -> %v", shelvedAt, cell(t, second, 0, "shelved_at"))
	}
}

// TestMediaDiaryListReorder pins the list's manual ordering: the detail carries the reorder slot,
// a drop's run renumbers only the open list's positions from the array order, and the detail
// returns its items in that order.
func TestMediaDiaryListReorder(t *testing.T) {
	deps, userID, bdbID := applyMediaDiary(t)

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	for _, f := range formulas {
		if f.Name == "list-detail" {
			if f.Display == nil || f.Display.Slots.Reorder == "" {
				t.Fatalf("list-detail display = %+v, want the reorder slot filled", f.Display)
			}
		}
	}

	runFormula(t, deps, userID, bdbID, "shelve-title", map[string]any{"title": "Dune", "kind": "book"})
	runFormula(t, deps, userID, bdbID, "shelve-title", map[string]any{"title": "Alien", "kind": "film"})
	runFormula(t, deps, userID, bdbID, "create-list", map[string]any{"name": "Canon"})
	runFormula(t, deps, userID, bdbID, "add-to-list",
		map[string]any{"parent_rowid": float64(1), "title_id": float64(1)})
	runFormula(t, deps, userID, bdbID, "add-to-list",
		map[string]any{"parent_rowid": float64(1), "title_id": float64(2)})

	// The drop reverses the pair: Alien first, Dune second.
	runFormula(t, deps, userID, bdbID, "reorder-list-items",
		map[string]any{"ordered_rowids": []any{float64(2), float64(1)}})
	detail := runFormula(t, deps, userID, bdbID, "list-detail", map[string]any{"rowid": float64(1)})
	first, _ := cell(t, detail.Result, 0, "heading").(string)
	second, _ := cell(t, detail.Result, 1, "heading").(string)
	if !strings.Contains(first, "Alien") || !strings.Contains(second, "Dune") {
		t.Errorf("list order after reorder = [%q, %q], want Alien then Dune", first, second)
	}
	positions := execSQL(t, deps, userID, bdbID,
		"SELECT id, position FROM list_items ORDER BY id", dbdomain.IntentQuery)
	if cell(t, positions, 0, "position") != int64(2) || cell(t, positions, 1, "position") != int64(1) {
		t.Errorf("positions after reorder = %v, want the renumbered pair", positions.Rows)
	}
}
