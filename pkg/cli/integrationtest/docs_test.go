//go:build integration

package integrationtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_Docs_Guide checks that docs, bare, prints the guide for agents without a data directory, creating none, and
// that the root help points agents at it.
func TestRun_Docs_Guide(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")

	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "docs")
	for _, want := range []string{"bdb bdbs describe <bdb>", "Read the\n   skills before changing anything",
		"bdb docs sql", "bdb docs formulas", "bdb changes undo <bdb>"} {
		if code != exitOK || !strings.Contains(stdout, want) {
			t.Errorf("docs = %d, stderr %q; want %q in %q", code, stderr, want, stdout)
		}
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Errorf("docs created the data directory (stat: %v), want it left alone", err)
	}

	if _, stdout, _ := runCLI(t, "bdb", "--help"); !strings.Contains(stdout, "An agent starts with `bdb docs`") {
		t.Errorf("bdb --help = %q, want it to point agents at bdb docs", stdout)
	}
}

// TestRun_Docs_Manuals checks that docs sql and docs formulas print the manuals naming each operation by its command,
// never by its MCP tool name, and that an unknown topic names the topics.
func TestRun_Docs_Manuals(t *testing.T) {
	dataDir := newKanban(t)
	tests := []struct {
		topic    string
		commands []string
		tools    []string
	}{
		{topic: "sql", commands: []string{"`bdb sql data`", "`bdb changes undo --anyone`"},
			tools: []string{"`execute_data`", "`undo_last_change_anyone`"}},
		{topic: "formulas", commands: []string{"`bdb formulas update`", "`bdb formulas show`", "`bdb formulas run`"},
			tools: []string{"`update_formula`", "`get_formula`", "`run_formula`"}},
	}
	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "docs", tt.topic)
			if code != exitOK || len(stdout) < 1000 {
				t.Fatalf("docs %s = %d (%d bytes), stderr %q; want the manual", tt.topic, code, len(stdout), stderr)
			}
			for _, command := range tt.commands {
				if !strings.Contains(stdout, command) {
					t.Errorf("docs %s does not name %s", tt.topic, command)
				}
			}
			for _, tool := range tt.tools {
				if strings.Contains(stdout, tool) {
					t.Errorf("docs %s still names the tool %s", tt.topic, tool)
				}
			}
		})
	}

	code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "docs", "recipes")
	if code != exitFailure || !strings.Contains(stderr, "'formulas', 'sql'") {
		t.Errorf("docs recipes = %d, stderr %q; want the topics named", code, stderr)
	}
}
