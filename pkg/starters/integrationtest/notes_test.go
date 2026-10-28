//go:build integration

package integrationtest

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
)

// applyNotes applies the embedded notes starter for a fresh user and returns the wired services with the
// new bdb's coordinates, ready for formula runs.
func applyNotes(t *testing.T) (deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	userID = uuid.New()
	bdbID, err = svc.CreateFromStarter(t.Context(), userID, testRoles, "notes", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter(notes) error = %v", err)
	}
	return deps, userID, bdbID
}

// TestNotesCaptureFindingFilesUnderResearch drives the research-capture shape end to end: the finding
// lands as a note titled by the question, tagged research, with the sources under their heading — and
// the feed's tag filter set to research lists exactly it, not the ordinary note captured alongside.
func TestNotesCaptureFindingFilesUnderResearch(t *testing.T) {
	deps, userID, bdbID := applyNotes(t)

	runFormula(t, deps, userID, bdbID, "capture", map[string]any{"body": "an ordinary quick capture"})
	runFormula(t, deps, userID, bdbID, "capture-finding", map[string]any{
		"question": "Which SQLite mode fits concurrent readers?",
		"result":   "WAL: readers never block the writer and vice versa.",
		"sources":  "https://sqlite.org/wal.html",
	})

	research := runFormula(t, deps, userID, bdbID, "notes-feed", map[string]any{"tag": "research"})
	if len(research.Result.Rows) != 1 {
		t.Fatalf("research-tagged rows = %d, want only the finding", len(research.Result.Rows))
	}
	if title := cell(t, research.Result, 0, "title"); title != "Which SQLite mode fits concurrent readers?" {
		t.Errorf("title = %v, want the question", title)
	}
	body, _ := cell(t, research.Result, 0, "body").(string)
	if !strings.Contains(body, "WAL: readers never block the writer") {
		t.Errorf("body = %q, want the distilled answer", body)
	}
	if !strings.Contains(body, "## Sources\n\nhttps://sqlite.org/wal.html") {
		t.Errorf("body = %q, want the sources under their heading", body)
	}

	tags := execSQL(t, deps, userID, bdbID,
		"SELECT tags FROM notes WHERE title IS NOT NULL", dbdomain.IntentQuery)
	if v := cell(t, tags, 0, "tags"); v != `["research"]` {
		t.Errorf("tags = %v, want the hard-coded research tag", v)
	}
}

// TestNotesCaptureFindingWithoutSources pins the optional-sources composition: an omitted sources param
// leaves the body as the bare answer, with no dangling Sources heading.
func TestNotesCaptureFindingWithoutSources(t *testing.T) {
	deps, userID, bdbID := applyNotes(t)

	runFormula(t, deps, userID, bdbID, "capture-finding", map[string]any{
		"question": "Is there a heading when sources are omitted?",
		"result":   "No.",
	})

	research := runFormula(t, deps, userID, bdbID, "notes-feed", map[string]any{"tag": "research"})
	body, _ := cell(t, research.Result, 0, "body").(string)
	if body != "No." {
		t.Errorf("body = %q, want the bare answer without a Sources heading", body)
	}
}

// TestNotesFeedSplitsUntitledBody pins the feed's first-line split for untitled notes: a one-line
// capture renders as its display title alone (an unsplit body would repeat the title on every quick
// capture), a multi-line capture keeps the remainder without its leading blank line, and a titled
// note's body passes through whole. The raw title and body ride along unsynthesized, so the edit
// dialog seeds what is stored — a no-op edit must not move the first line into the title.
func TestNotesFeedSplitsUntitledBody(t *testing.T) {
	deps, userID, bdbID := applyNotes(t)

	runFormula(t, deps, userID, bdbID, "capture", map[string]any{"body": "just one line"})
	runFormula(t, deps, userID, bdbID, "capture", map[string]any{"body": "first line\n\nthe rest"})
	runFormula(t, deps, userID, bdbID, "capture",
		map[string]any{"title": "titled", "body": "kept\nwhole"})

	feed := runFormula(t, deps, userID, bdbID, "notes-feed", nil)
	if len(feed.Result.Rows) != 3 {
		t.Fatalf("feed rows = %d, want 3", len(feed.Result.Rows))
	}
	type rawRow struct{ body, rawTitle, rawBody any }
	byTitle := map[string]rawRow{}
	for i := range feed.Result.Rows {
		title, _ := cell(t, feed.Result, i, "display_title").(string)
		byTitle[title] = rawRow{
			body:     cell(t, feed.Result, i, "display_body"),
			rawTitle: cell(t, feed.Result, i, "title"),
			rawBody:  cell(t, feed.Result, i, "body"),
		}
	}
	oneLiner, ok := byTitle["just one line"]
	if !ok || oneLiner.body != "" {
		t.Errorf("one-liner display body = %v, want empty — the line already is the title", oneLiner.body)
	}
	if oneLiner.rawTitle != nil || oneLiner.rawBody != "just one line" {
		t.Errorf("one-liner raw title/body = %v/%v, want NULL and the stored body for the edit seed",
			oneLiner.rawTitle, oneLiner.rawBody)
	}
	if row := byTitle["first line"]; row.body != "the rest" || row.rawBody != "first line\n\nthe rest" {
		t.Errorf("multi-line display/raw body = %v/%v, want the split remainder and the whole stored body",
			row.body, row.rawBody)
	}
	if row := byTitle["titled"]; row.body != "kept\nwhole" || row.rawTitle != "titled" {
		t.Errorf("titled display body / raw title = %v/%v, want them untouched", row.body, row.rawTitle)
	}

	// Pinning prefixes the display title with the pin mark — and only the display title, so the
	// edit dialog's raw seed never picks it up.
	rowid := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM notes WHERE title = 'titled'", dbdomain.IntentQuery), 0, "id")
	runFormula(t, deps, userID, bdbID, "toggle-pin", map[string]any{"rowid": float64(rowid.(int64))})
	pinned := runFormula(t, deps, userID, bdbID, "notes-feed", nil)
	if title := cell(t, pinned.Result, 0, "display_title"); title != "📌 titled" {
		t.Errorf("pinned display title = %v, want the pin prefix", title)
	}
	if raw := cell(t, pinned.Result, 0, "title"); raw != "titled" {
		t.Errorf("pinned raw title = %v, want it unprefixed for the edit seed", raw)
	}
}

// TestNotesEditNoteRewritesTags drives the whole-note edit round trip: the group's {tag: …} rows
// recompose the stored string array (trimmed, blank rows dropped), the feed exposes the group-shaped
// tags_rows column the edit dialog seeds from, and an edit omitting the group clears the tags — the
// formula rewrites the note whole.
func TestNotesEditNoteRewritesTags(t *testing.T) {
	deps, userID, bdbID := applyNotes(t)

	runFormula(t, deps, userID, bdbID, "capture", map[string]any{"body": "a note"})
	idCell := cell(t, execSQL(t, deps, userID, bdbID, "SELECT id FROM notes", dbdomain.IntentQuery), 0, "id")
	rowid := float64(idCell.(int64))

	runFormula(t, deps, userID, bdbID, "edit-note", map[string]any{
		"rowid": rowid, "body": "a note",
		"tags": []any{
			map[string]any{"tag": " work "},
			map[string]any{"tag": "   "},
			map[string]any{"tag": "ideas"},
		},
	})
	tags := execSQL(t, deps, userID, bdbID, "SELECT tags FROM notes", dbdomain.IntentQuery)
	if v := cell(t, tags, 0, "tags"); v != `["work","ideas"]` {
		t.Errorf("tags = %v, want the trimmed array without the blank row", v)
	}

	feed := runFormula(t, deps, userID, bdbID, "notes-feed", nil)
	if v, _ := cell(t, feed.Result, 0, "tags").(string); v != `[{"tag":"work"},{"tag":"ideas"}]` {
		t.Errorf("feed tags = %q, want the group-shaped seed column", v)
	}

	runFormula(t, deps, userID, bdbID, "edit-note", map[string]any{"rowid": rowid, "body": "a note"})
	tags = execSQL(t, deps, userID, bdbID, "SELECT tags FROM notes", dbdomain.IntentQuery)
	if v := cell(t, tags, 0, "tags"); v != `[]` {
		t.Errorf("tags after a group-less edit = %v, want cleared — the edit rewrites the note whole", v)
	}
}

// TestNotesFeedFilters pins the feed's composable filters: an unfiltered run lists everything, the
// full-text query narrows to matching notes while keeping their normal cards, a blank query means
// everything rather than nothing, and query + tag compose.
func TestNotesFeedFilters(t *testing.T) {
	deps, userID, bdbID := applyNotes(t)

	runFormula(t, deps, userID, bdbID, "capture", map[string]any{"body": "shopping for lockstep gears"})
	runFormula(t, deps, userID, bdbID, "capture-finding", map[string]any{
		"question": "How do FTS triggers stay in sync?",
		"result":   "External-content mode with three lockstep triggers.",
	})
	// The ordinary note gets a tag of its own, so the tag filter must match the requested tag —
	// a predicate accepting any tagged note would wrongly include it.
	plainID := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM notes WHERE title IS NULL", dbdomain.IntentQuery), 0, "id")
	runFormula(t, deps, userID, bdbID, "edit-note", map[string]any{
		"rowid": float64(plainID.(int64)), "body": "shopping for lockstep gears",
		"tags": []any{map[string]any{"tag": "shopping"}},
	})

	if all := runFormula(t, deps, userID, bdbID, "notes-feed", nil); len(all.Result.Rows) != 2 {
		t.Fatalf("unfiltered rows = %d, want 2", len(all.Result.Rows))
	}
	if blank := runFormula(t, deps, userID, bdbID, "notes-feed",
		map[string]any{"query": "   "}); len(blank.Result.Rows) != 2 {
		t.Errorf("blank-query rows = %d, want everything rather than nothing", len(blank.Result.Rows))
	}
	if hits := runFormula(t, deps, userID, bdbID, "notes-feed",
		map[string]any{"query": "lockstep"}); len(hits.Result.Rows) != 2 {
		t.Errorf("query rows = %d, want both notes mentioning lockstep", len(hits.Result.Rows))
	}
	combined := runFormula(t, deps, userID, bdbID, "notes-feed",
		map[string]any{"query": "lockstep", "tag": "research"})
	if len(combined.Result.Rows) != 1 {
		t.Fatalf("query+tag rows = %d, want only the finding", len(combined.Result.Rows))
	}
	if title := cell(t, combined.Result, 0, "title"); title != "How do FTS triggers stay in sync?" {
		t.Errorf("combined filter title = %v, want the finding", title)
	}
}

// TestNotesCaptureWithTags pins the add-side tags group: rows recompose the stored string array (trimmed,
// blank rows dropped) exactly like the edit, and a capture without the group stores an empty array.
func TestNotesCaptureWithTags(t *testing.T) {
	deps, userID, bdbID := applyNotes(t)

	runFormula(t, deps, userID, bdbID, "capture", map[string]any{
		"body": "tagged at birth",
		"tags": []any{map[string]any{"tag": " work "}, map[string]any{"tag": "   "}},
	})
	runFormula(t, deps, userID, bdbID, "capture", map[string]any{"body": "untagged"})

	tagged := execSQL(t, deps, userID, bdbID,
		"SELECT tags FROM notes WHERE body = 'tagged at birth'", dbdomain.IntentQuery)
	if v := cell(t, tagged, 0, "tags"); v != `["work"]` {
		t.Errorf("tags = %v, want the trimmed array without the blank row", v)
	}
	untagged := execSQL(t, deps, userID, bdbID,
		"SELECT tags FROM notes WHERE body = 'untagged'", dbdomain.IntentQuery)
	if v := cell(t, untagged, 0, "tags"); v != `[]` {
		t.Errorf("tags without the group = %v, want an empty array", v)
	}

	// Each tag row is a creatable picker: existing tags surface as suggestions, typing past them creates.
	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	for _, f := range formulas {
		if f.Name != "capture" && f.Name != "edit-note" {
			continue
		}
		found := false
		for _, p := range f.Params {
			if p.Name != "tags" {
				continue
			}
			for _, field := range p.Fields {
				if field.Name == "tag" {
					found = field.Lookup != nil && field.Lookup.AllowNew && field.Lookup.Table == "tag_options"
				}
			}
		}
		if !found {
			t.Errorf("%s tag field: want an allow_new lookup over tag_options", f.Name)
		}
	}
}
