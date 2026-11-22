//go:build integration

package integrationtest

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
)

// applyAIMemory applies the embedded ai-memory starter for a fresh user and returns the wired services
// with the new bdb's coordinates, ready for formula runs.
func applyAIMemory(t *testing.T) (deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	userID = uuid.New()
	bdbID, err = svc.CreateFromStarter(t.Context(), userID, testRoles, "ai-memory", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter(ai-memory) error = %v", err)
	}
	return deps, userID, bdbID
}

// TestAIMemoryRememberUpsertKeepsOmittedFields drives the remember upsert round trip: the insert lands
// with type, tags, scope and source, and a later upsert of the same slug that omits every optional field
// rewrites summary/content while keeping the stored type, tags, scope and source untouched.
func TestAIMemoryRememberUpsertKeepsOmittedFields(t *testing.T) {
	deps, userID, bdbID := applyAIMemory(t)

	runFormula(t, deps, userID, bdbID, "remember", map[string]any{
		"slug": "prefers-dark-mode", "summary": "prefers dark mode everywhere",
		"content": "Dark mode in every tool that offers it.",
		"type":    "preference", "tags": `["ui"]`, "scope": "claude-code", "source": "claude-code",
	})
	runFormula(t, deps, userID, bdbID, "remember", map[string]any{
		"slug": "prefers-dark-mode", "summary": "prefers dark mode, light only for print",
		"content": "Dark mode everywhere; light theme only when printing.",
	})

	row := execSQL(t, deps, userID, bdbID,
		"SELECT summary, type, tags, scope, source FROM memories WHERE slug = 'prefers-dark-mode'",
		dbdomain.IntentQuery)
	if v := cell(t, row, 0, "summary"); v != "prefers dark mode, light only for print" {
		t.Errorf("summary = %v, want the rewritten one", v)
	}
	if v := cell(t, row, 0, "type"); v != "preference" {
		t.Errorf("type = %v, want kept on omission", v)
	}
	if v := cell(t, row, 0, "tags"); v != `["ui"]` {
		t.Errorf("tags = %v, want kept on omission", v)
	}
	if v := cell(t, row, 0, "scope"); v != "claude-code" {
		t.Errorf("scope = %v, want kept on omission", v)
	}
	if v := cell(t, row, 0, "source"); v != "claude-code" {
		t.Errorf("source = %v, want kept on omission", v)
	}
}

// TestAIMemoryTypeVocabularyEnforced pins the enforced type enum: a remember run with a type outside
// the vocabulary is rejected naming the param, nothing is written, and the same guard holds on
// edit-memory and by-type.
func TestAIMemoryTypeVocabularyEnforced(t *testing.T) {
	deps, userID, bdbID := applyAIMemory(t)

	_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "remember",
		dbservice.FormulaRunInput{Params: map[string]any{
			"slug": "typo-type", "summary": "s", "content": "c", "type": "prefernce",
		}})
	if err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("remember with a typo type: err = %v, want a rejection naming the param", err)
	}
	count := execSQL(t, deps, userID, bdbID, "SELECT COUNT(*) AS n FROM memories", dbdomain.IntentQuery)
	if v := cell(t, count, 0, "n"); v != int64(0) {
		t.Errorf("memories after the rejected run = %v, want none written", v)
	}

	runFormula(t, deps, userID, bdbID, "remember",
		map[string]any{"slug": "a-fact", "summary": "s", "content": "c", "type": "fact"})
	rowid := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM memories WHERE slug = 'a-fact'", dbdomain.IntentQuery), 0, "id")
	_, err = deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "edit-memory",
		dbservice.FormulaRunInput{Params: map[string]any{
			"rowid": float64(rowid.(int64)), "slug": "a-fact", "summary": "s", "content": "c", "type": "facts",
		}})
	if err == nil || !strings.Contains(err.Error(), "type") {
		t.Errorf("edit-memory with an off-vocabulary type: err = %v, want a rejection naming the param", err)
	}
	if _, err = deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "by-type",
		dbservice.FormulaRunInput{Params: map[string]any{"type": "unknown"}}); err == nil {
		t.Errorf("by-type with an off-vocabulary type: err = nil, want a rejection")
	}
	if hits := runFormula(t, deps, userID, bdbID, "by-type",
		map[string]any{"type": "fact"}); len(hits.Result.Rows) != 1 {
		t.Errorf("by-type(fact) rows = %d, want the one fact", len(hits.Result.Rows))
	}
}

// TestAIMemoryEditDefaultsAndCaps pins the declared defaults: an edit omitting tags and scope resets
// them to '[]' / 'global' (the edit rewrites the memory whole, unlike remember's keep-on-omit), and
// boot / recall bind their default cap while an explicit max_rows narrows them.
func TestAIMemoryEditDefaultsAndCaps(t *testing.T) {
	deps, userID, bdbID := applyAIMemory(t)

	runFormula(t, deps, userID, bdbID, "remember", map[string]any{
		"slug": "workspace-layout", "summary": "keeps terminals on the left monitor",
		"content": "Terminals left, browser right.", "tags": `["workspace"]`, "scope": "claude-code",
	})
	rowid := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM memories WHERE slug = 'workspace-layout'", dbdomain.IntentQuery), 0, "id")
	runFormula(t, deps, userID, bdbID, "edit-memory", map[string]any{
		"rowid": float64(rowid.(int64)), "slug": "workspace-layout",
		"summary": "keeps terminals on the left monitor", "content": "Terminals left, browser right.",
	})
	row := execSQL(t, deps, userID, bdbID,
		"SELECT tags, scope FROM memories WHERE slug = 'workspace-layout'", dbdomain.IntentQuery)
	if v := cell(t, row, 0, "tags"); v != `[]` {
		t.Errorf("tags after a tag-less edit = %v, want the default empty array", v)
	}
	if v := cell(t, row, 0, "scope"); v != "global" {
		t.Errorf("scope after a scope-less edit = %v, want the default global", v)
	}

	runFormula(t, deps, userID, bdbID, "remember", map[string]any{
		"slug": "second-memory", "summary": "a second searchable memory", "content": "Also mentions terminals.",
	})
	if all := runFormula(t, deps, userID, bdbID, "boot", nil); len(all.Result.Rows) != 2 {
		t.Errorf("boot rows = %d, want both under the default cap", len(all.Result.Rows))
	}
	if capped := runFormula(t, deps, userID, bdbID, "boot",
		map[string]any{"max_rows": 1.0}); len(capped.Result.Rows) != 1 {
		t.Errorf("boot(max_rows=1) rows = %d, want 1", len(capped.Result.Rows))
	}
	if hits := runFormula(t, deps, userID, bdbID, "recall",
		map[string]any{"query": "terminals"}); len(hits.Result.Rows) != 2 {
		t.Errorf("recall rows = %d, want both matches under the default cap", len(hits.Result.Rows))
	}
	if capped := runFormula(t, deps, userID, bdbID, "recall",
		map[string]any{"query": "terminals", "max_rows": 1.0}); len(capped.Result.Rows) != 1 {
		t.Errorf("recall(max_rows=1) rows = %d, want 1", len(capped.Result.Rows))
	}
}

// TestAIMemoryAmendMemoryPatchesContent checks that amend-memory replaces the one passage of a memory's content,
// leaves its summary alone and restarts its staleness clock, as any rewrite of the content does.
func TestAIMemoryAmendMemoryPatchesContent(t *testing.T) {
	deps, userID, bdbID := applyAIMemory(t)

	runFormula(t, deps, userID, bdbID, "remember", map[string]any{
		"slug": "bakery-oven-schedule", "summary": "the bakery's oven schedule",
		"content": "The first batch goes in at 5:00.\nThe sourdough proofs overnight.",
	})
	idCell := cell(t, execSQL(t, deps, userID, bdbID, "SELECT id FROM memories", dbdomain.IntentQuery), 0, "id")
	memoryID := float64(idCell.(int64))
	runFormula(t, deps, userID, bdbID, "reaffirm", map[string]any{"rowid": memoryID})

	runFormula(t, deps, userID, bdbID, "amend-memory", map[string]any{
		"memory_id": memoryID, "old": "at 5:00", "new": "at 4:30",
	})

	row := execSQL(t, deps, userID, bdbID,
		"SELECT summary, content, verified_at FROM memories WHERE slug = 'bakery-oven-schedule'", dbdomain.IntentQuery)
	if v := cell(t, row, 0, "content"); v != "The first batch goes in at 4:30.\nThe sourdough proofs overnight." {
		t.Errorf("content = %q, want the one passage replaced", v)
	}
	if v := cell(t, row, 0, "summary"); v != "the bakery's oven schedule" {
		t.Errorf("summary = %v, want it untouched", v)
	}
	if v := cell(t, row, 0, "verified_at"); v != nil {
		t.Errorf("verified_at = %v, want it cleared so the staleness clock restarts", v)
	}

	_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "amend-memory", dbservice.FormulaRunInput{
		Params: map[string]any{"memory_id": memoryID, "old": "croissants", "new": "x"},
	})
	if err == nil || !strings.Contains(err.Error(), "text_patch: old not found") {
		t.Errorf("amending an absent passage: error = %v, want it refused", err)
	}
}

// TestAIMemoryRecallTakesPunctuation checks that recall finds a memory by words carrying punctuation, a hyphenated
// name and a time of day, which raw FTS5 would read as syntax and fail on, and that every word has to be found.
func TestAIMemoryRecallTakesPunctuation(t *testing.T) {
	deps, userID, bdbID := applyAIMemory(t)

	runFormula(t, deps, userID, bdbID, "remember", map[string]any{
		"slug": "bakery-oven-schedule", "summary": "the bakery's deck-oven schedule",
		"content": "The deck-oven (no.2) takes the first batch at 5:00.",
	})
	runFormula(t, deps, userID, bdbID, "remember", map[string]any{
		"slug": "bakery-shop-window", "summary": "the bakery's shop window",
		"content": "Repainted in spring; the deck chairs stay outside.",
	})

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"deck-oven", []string{"bakery-oven-schedule"}},
		{"5:00", []string{"bakery-oven-schedule"}},
		{"deck spring", []string{"bakery-shop-window"}},
		{"deck croissant", nil},
	} {
		found := runFormula(t, deps, userID, bdbID, "recall", map[string]any{"query": tc.query})
		var got []string
		for row := range found.Result.Rows {
			slug, _ := cell(t, found.Result, row, "slug").(string)
			got = append(got, slug)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("recall %q found %q, want %q", tc.query, got, tc.want)
		}
	}
}
