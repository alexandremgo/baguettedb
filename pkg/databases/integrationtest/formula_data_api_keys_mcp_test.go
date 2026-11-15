//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// keyedBdbsApp is a BdbsApp allowing every access, which answers keys as the bdb's Data API keys by formula ID, or
// fails with err.
type keyedBdbsApp struct {
	allowAllBdbsApp
	mu   sync.Mutex
	keys map[string][]apptypes.DataAPIKey
	err  error
}

// setKeys makes the double answer keys, or fail with err.
func (a *keyedBdbsApp) setKeys(keys map[string][]apptypes.DataAPIKey, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keys, a.err = keys, err
}

// ListFormulaDataAPIKeys answers the double's keys, or its error.
func (a *keyedBdbsApp) ListFormulaDataAPIKeys(
	_ context.Context, _, _ uuid.UUID,
) (map[string][]apptypes.DataAPIKey, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keys, a.err
}

// formulaKeyNames returns the names of the Data API keys a formula payload carries, in order, failing the test when a
// key carries anything but its ID and name.
func formulaKeyNames(t *testing.T, formula map[string]any) []string {
	t.Helper()
	raw, _ := formula["data_api_keys"].([]any)
	names := make([]string, 0, len(raw))
	for _, k := range raw {
		key, _ := k.(map[string]any)
		name, _ := key["name"].(string)
		if _, hasID := key["id"].(string); !hasID || len(key) != 2 {
			t.Errorf("key %v, want its ID and name alone", key)
		}
		names = append(names, name)
	}
	return names
}

// toolText returns the text of a tool result that isn't an error.
func toolText(t *testing.T, c *mcpclient.Client, name string, args map[string]any) string {
	t.Helper()
	result := callTool(t, c, name, args)
	if result.IsError || len(result.Content) == 0 {
		t.Fatalf("tool %s = %v, want a text answer", name, result.Content)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("tool %s: expected TextContent, got %T", name, result.Content[0])
	}
	return text.Text
}

// TestMCP_FormulaDataAPIKeys checks how the formula tools show the Data API keys apps run a formula with: get_formula
// and list_formulas name each formula's keys, by ID and name alone, and a formula on no key carries none;
// update_formula adds a note naming them; and delete_formula's answer names the keys that can no longer run it.
func TestMCP_FormulaDataAPIKeys(t *testing.T) {
	truncateAll(t)
	bdbID := uuid.New().String()
	app := &keyedBdbsApp{}
	c := startMCPClient(t, setupRouterWithBdbsApp(t, app), uuid.New().String())
	seedGroceriesMCP(t, c, bdbID)
	onKeys := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "all-groceries", "description": "Lists groceries.",
		"sql": "SELECT name FROM groceries",
	})
	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "grocery-count", "description": "Counts groceries.",
		"sql": "SELECT COUNT(*) AS n FROM groceries",
	})
	onKeysID, _ := onKeys["id"].(string)
	app.setKeys(map[string][]apptypes.DataAPIKey{onKeysID: {
		{ID: uuid.New(), Name: "shopping app", TokenPrefix: "bdbdk_abcd"},
		{ID: uuid.New(), Name: "fridge screen", TokenPrefix: "bdbdk_efgh"},
	}}, nil)
	wantNames := []string{"shopping app", "fridge screen"}

	got := callFormulaTool(t, c, "get_formula", map[string]any{"bdb_id": bdbID, "ref": "all-groceries"})
	if names := formulaKeyNames(t, got); !slices.Equal(names, wantNames) {
		t.Errorf("get_formula's keys = %v, want %v", names, wantNames)
	}
	for _, raw := range listFormulasViaMCP(t, c, bdbID) {
		f, _ := raw.(map[string]any)
		want := wantNames
		if f["name"] == "grocery-count" {
			want = []string{}
		}
		if names := formulaKeyNames(t, f); !slices.Equal(names, want) {
			t.Errorf("list_formulas' keys of %v = %v, want %v", f["name"], names, want)
		}
	}

	updated := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID, "ref": "all-groceries", "description": "Lists every grocery.",
	})
	if note, _ := updated["note"].(string); !strings.Contains(note, `"shopping app", "fridge screen"`) {
		t.Errorf("update_formula's note = %q, want the keys named", note)
	}
	plain := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID, "ref": "grocery-count", "description": "Counts every grocery.",
	})
	if _, hasNote := plain["note"]; hasNote {
		t.Errorf("update_formula of a formula on no key = %v, want no note", plain)
	}

	deleted := toolText(t, c, "delete_formula", map[string]any{"bdb_id": bdbID, "ref": "all-groceries"})
	if !strings.Contains(deleted, `"shopping app", "fridge screen"`) {
		t.Errorf("delete_formula's answer = %q, want the keys named", deleted)
	}
	plainDeleted := toolText(t, c, "delete_formula", map[string]any{"bdb_id": bdbID, "ref": "grocery-count"})
	if strings.Contains(plainDeleted, "Data API") {
		t.Errorf("delete_formula's answer for a formula on no key = %q, want no keys named", plainDeleted)
	}
}

// TestMCP_FormulaDataAPIKeys_LookupFails checks that the formula tools still answer when the keys can't be looked up:
// get_formula and list_formulas give the formula without keys, and update_formula and delete_formula succeed.
func TestMCP_FormulaDataAPIKeys_LookupFails(t *testing.T) {
	truncateAll(t)
	bdbID := uuid.New().String()
	app := &keyedBdbsApp{}
	app.setKeys(nil, errors.New("bdbs unreachable"))
	c := startMCPClient(t, setupRouterWithBdbsApp(t, app), uuid.New().String())
	seedGroceriesMCP(t, c, bdbID)
	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "all-groceries", "description": "Lists groceries.",
		"sql": "SELECT name FROM groceries",
	})

	got := callFormulaTool(t, c, "get_formula", map[string]any{"bdb_id": bdbID, "ref": "all-groceries"})
	if _, hasKeys := got["data_api_keys"]; hasKeys || got["name"] != "all-groceries" {
		t.Errorf("get_formula = %v, want the formula without keys", got)
	}
	if listed := listFormulasViaMCP(t, c, bdbID); len(listed) != 1 {
		t.Errorf("list_formulas = %v, want the formula", listed)
	}
	callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID, "ref": "all-groceries", "description": "Lists every grocery.",
	})
	toolText(t, c, "delete_formula", map[string]any{"bdb_id": bdbID, "ref": "all-groceries"})
}
