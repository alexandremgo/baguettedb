//go:build integration

package integrationtest

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// formulaKeyNames returns the sorted names of the Data API keys a formula of a --json answer carries.
func formulaKeyNames(formula map[string]any) []string {
	keys, _ := formula["data_api_keys"].([]any)
	names := []string{}
	for _, raw := range keys {
		key, _ := raw.(map[string]any)
		name, _ := key["name"].(string)
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// TestRun_FormulaDataAPIKeys checks how the formula commands show the Data API keys apps run a formula with, keys
// created over bdb serve: formulas list and bdbs describe add a KEYS column, and --json a data_api_keys field, for the
// formula on keys alone; formulas show adds a DATA API KEYS section, and --json the field; formulas update notes on
// stderr that the apps run the new version, stdout keeping its one line, and --json carries the note; formulas delete
// names on stderr the keys that can no longer run it. A formula on no key shows none of this.
func TestRun_FormulaDataAPIKeys(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	bdb := func(stdin string, args ...string) (string, string) {
		t.Helper()
		code, stdout, stderr := runCLIWithStdin(t, stdin, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
		if code != exitOK {
			t.Fatalf("%v = %d %q, stderr %q", args, code, stdout, stderr)
		}
		return stdout, stderr
	}
	token, _ := bdb("", "tokens", "create", "owner-laptop")
	created, _ := bdb("", "bdbs", "create", "Notes", "--json")
	var notes struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created), &notes); err != nil {
		t.Fatal(err)
	}
	bdb("", "sql", "schema", "Notes", "CREATE TABLE notes (body TEXT)")
	bdb(`{"name": "all-notes", "description": "Lists the notes", "sql": "SELECT body FROM notes"}`,
		"formulas", "create", "Notes", "--file", "-")
	bdb(`{"name": "note-count", "description": "Counts the notes", "sql": "SELECT COUNT(*) AS n FROM notes"}`,
		"formulas", "create", "Notes", "--file", "-")

	listJSON := func() map[string]map[string]any {
		t.Helper()
		out, _ := bdb("", "formulas", "list", "Notes", "--json")
		var formulas []map[string]any
		if err := json.Unmarshal([]byte(out), &formulas); err != nil {
			t.Fatalf("decoding formulas list --json %q: %v", out, err)
		}
		byName := make(map[string]map[string]any, len(formulas))
		for _, f := range formulas {
			name, _ := f["name"].(string)
			byName[name] = f
		}
		return byName
	}
	allNotesID, _ := listJSON()["all-notes"]["id"].(string)

	baseURL, _, stop := startServe(t, dataDir)
	for _, name := range []string{"phone app", "web app"} {
		if status, body := withBearer(t, http.MethodPost, baseURL+"/api/bdbs/v1/bdbs/"+notes.ID+"/data-api-keys",
			strings.TrimSpace(token), map[string]any{"name": name, "formula_ids": []string{allNotesID}}); status !=
			http.StatusCreated {
			t.Fatalf("creating the key %s = %d %s, want 201", name, status, body)
		}
	}
	stop()
	want := []string{"phone app", "web app"}

	listed := listJSON()
	if names := formulaKeyNames(listed["all-notes"]); !slices.Equal(names, want) {
		t.Errorf("formulas list --json keys of all-notes = %v, want %v", names, want)
	}
	if _, hasKeys := listed["note-count"]["data_api_keys"]; hasKeys {
		t.Errorf("formulas list --json note-count = %v, want no keys", listed["note-count"])
	}
	if out, _ := bdb("", "formulas", "list", "Notes"); !strings.Contains(out, "KEYS") ||
		!strings.Contains(out, "phone app, web app") && !strings.Contains(out, "web app, phone app") {
		t.Errorf("formulas list = %q, want a KEYS column naming the two keys", out)
	}
	if out, _ := bdb("", "bdbs", "describe", "Notes"); !strings.Contains(out, "KEYS") ||
		!strings.Contains(out, "phone app") || !strings.Contains(out, "web app") {
		t.Errorf("bdbs describe = %q, want its formulas with a KEYS column naming the two keys", out)
	}
	describedJSON, _ := bdb("", "bdbs", "describe", "Notes", "--json")
	var described struct {
		Formulas []map[string]any `json:"formulas"`
	}
	if err := json.Unmarshal([]byte(describedJSON), &described); err != nil || len(described.Formulas) != 2 {
		t.Fatalf("bdbs describe --json = %q (%v), want both formulas", describedJSON, err)
	}
	for _, f := range described.Formulas {
		wantKeys := []string{}
		if f["name"] == "all-notes" {
			wantKeys = want
		}
		if names := formulaKeyNames(f); !slices.Equal(names, wantKeys) {
			t.Errorf("bdbs describe --json keys of %v = %v, want %v", f["name"], names, wantKeys)
		}
	}

	out, _ := bdb("", "formulas", "show", "Notes", "all-notes", "--json")
	var shown map[string]any
	if err := json.Unmarshal([]byte(out), &shown); err != nil || !slices.Equal(formulaKeyNames(shown), want) {
		t.Errorf("formulas show --json = %q (%v), want the two keys", out, err)
	}
	if out, _ := bdb("", "formulas", "show", "Notes", "all-notes"); !strings.Contains(out, "DATA API KEYS") ||
		!strings.Contains(out, "phone app") || !strings.Contains(out, "web app") {
		t.Errorf("formulas show = %q, want a DATA API KEYS section naming the two keys", out)
	}
	if out, _ := bdb("", "formulas", "show", "Notes", "note-count"); strings.Contains(out, "DATA API KEYS") {
		t.Errorf("formulas show note-count = %q, want no keys section", out)
	}

	stdout, stderr := bdb(`{"description": "Lists every note"}`,
		"formulas", "update", "Notes", "all-notes", "--file", "-")
	if stdout != "Updated formula all-notes in Notes (view)\n" || !strings.Contains(stderr, `"phone app"`) ||
		!strings.Contains(stderr, `"web app"`) || !strings.Contains(stderr, "new version") {
		t.Errorf("formulas update = %q, stderr %q; want its one line, and a note naming the keys on stderr", stdout,
			stderr)
	}
	out, _ = bdb(`{"description": "Lists all the notes"}`, "formulas", "update", "Notes", "all-notes", "--file", "-",
		"--json")
	var updated map[string]any
	if err := json.Unmarshal([]byte(out), &updated); err != nil {
		t.Fatalf("decoding formulas update --json %q: %v", out, err)
	}
	if note, _ := updated["note"].(string); !strings.Contains(note, `"phone app"`) {
		t.Errorf("formulas update --json note = %q, want the keys named", note)
	}
	if _, stderr := bdb(`{"description": "Counts every note"}`, "formulas", "update", "Notes", "note-count",
		"--file", "-"); stderr != "" {
		t.Errorf("formulas update of a formula on no key: stderr %q, want none", stderr)
	}

	stdout, stderr = bdb("", "formulas", "delete", "Notes", "all-notes")
	if stdout != "Deleted formula all-notes from Notes\n" || !strings.Contains(stderr, `"phone app"`) ||
		!strings.Contains(stderr, "can no longer run it") {
		t.Errorf("formulas delete = %q, stderr %q; want its one line, and the keys named on stderr", stdout, stderr)
	}
	if _, stderr := bdb("", "formulas", "delete", "Notes", "note-count"); stderr != "" {
		t.Errorf("formulas delete of a formula on no key: stderr %q, want none", stderr)
	}
}
