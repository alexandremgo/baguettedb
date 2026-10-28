//go:build integration

package integrationtest

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_StartersList checks that starters list prints the catalog as a table with each starter's key, name and
// description, and as JSON with tags, an array even for a starter without any; the bare starters command prints the
// same table.
func TestRun_StartersList(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "starters", "list")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != exitOK || len(lines) < 2 || strings.Join(strings.Fields(lines[0]), " ") != "KEY NAME DESCRIPTION" {
		t.Fatalf("starters list = %d %q, stderr %q; want a header and the starters", code, stdout, stderr)
	}
	if !hasRowPrefix(lines[1:], []string{"kanban", "Kanban"}) {
		t.Errorf("starters list %q has no kanban row", stdout)
	}
	if _, bare, _ := runCLI(t, "bdb", "--data-dir", dataDir, "starters"); bare != stdout {
		t.Errorf("bare starters = %q, want the starters list output", bare)
	}

	code, stdout, _ = runCLI(t, "bdb", "--data-dir", dataDir, "starters", "list", "--json")
	var starters []struct {
		Key         string   `json:"key"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(stdout), &starters); code != exitOK || err != nil || len(starters) != len(lines)-1 {
		t.Fatalf("starters list --json = %d %q (%v), want the %d starters of the table", code, stdout, err,
			len(lines)-1)
	}
	for _, s := range starters {
		if s.Key == "" || s.Name == "" || s.Description == "" || s.Tags == nil {
			t.Errorf("starter %+v lacks a key, name, description or tags array", s)
		}
	}
}

// hasRowPrefix reports whether one of lines starts with the whitespace-separated fields want.
func hasRowPrefix(lines []string, want []string) bool {
	for _, line := range lines {
		if fields := strings.Fields(line); len(fields) >= len(want) &&
			strings.Join(fields[:len(want)], " ") == strings.Join(want, " ") {
			return true
		}
	}
	return false
}
