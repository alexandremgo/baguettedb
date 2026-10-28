//go:build integration

package integrationtest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_Version checks that the version command and the --version flag print the same line, named after the
// binary the command line was started as.
func TestRun_Version(t *testing.T) {
	var versions []string
	for _, args := range [][]string{
		{"bdb", "version"},
		{"bdb", "--version"},
		{"/usr/local/bin/baguettedb", "version"},
	} {
		code, stdout, stderr := runCLI(t, args...)
		v, named := strings.CutPrefix(stdout, filepath.Base(args[0])+" ")
		if code != exitOK || !named || v == "\n" {
			t.Errorf("%v = %d %q (stderr %q), want %d and %q followed by the version", args, code, stdout, stderr,
				exitOK, filepath.Base(args[0]))
		}
		versions = append(versions, v)
	}
	if versions[0] != versions[1] || versions[1] != versions[2] {
		t.Errorf("versions = %q, want the same version from every form", versions)
	}
}

// TestRun_UsageErrors checks that an unknown command, an unknown flag, an unexpected argument, an invalid flag value
// and an empty --data-dir all end with the usage exit status and a pointer to the help of the command that failed.
func TestRun_UsageErrors(t *testing.T) {
	for _, tt := range []struct {
		args     []string
		helpPath string
	}{
		{[]string{"bdb", "nope"}, "bdb"},
		{[]string{"bdb", "--nope"}, "bdb"},
		{[]string{"bdb", "version", "extra"}, "bdb version"},
		{[]string{"bdb", "--log-level", "loud", "mcp"}, "bdb mcp"},
		{[]string{"bdb", "mcp", "config", "--nope"}, "bdb mcp config"},
		{[]string{"bdb", "--data-dir", "", "bdbs"}, "bdb bdbs"},
	} {
		code, _, stderr := runCLI(t, tt.args...)
		if want := "Run `" + tt.helpPath + " --help` for usage."; code != exitUsage || !strings.Contains(stderr, want) {
			t.Errorf("%v = %d, stderr %q; want %d and %q", tt.args, code, stderr, exitUsage, want)
		}
	}
}

// TestRun_UnknownCommandSuggests checks that a mistyped command, a missing letter or two swapped ones, is refused
// with the command it is close to.
func TestRun_UnknownCommandSuggests(t *testing.T) {
	//nolint:misspell // "chnages" is the mistyped command under test
	for _, tt := range []struct{ typed, want string }{{"formula", "formulas"}, {"chnages", "changes"}} {
		code, _, stderr := runCLI(t, "bdb", tt.typed)
		if want := "\n\nDid you mean this?\n\t" + tt.want + "\n"; code != exitUsage || !strings.Contains(stderr, want) {
			t.Errorf("bdb %s = %d, stderr %q; want %d and %q", tt.typed, code, stderr, exitUsage, want)
		}
	}
}

// TestRun_ArgumentErrors checks that wrong positional arguments are usage errors that say what is wrong in the terms
// of the command's usage line: the missing argument, what each given one was read as and where to find a value; a
// subcommand given after an argument; too many arguments, with the close subcommands for a group; and that a group's
// usage line shows its list's arguments.
func TestRun_ArgumentErrors(t *testing.T) {
	bdbHint := "<bdb> is the bdb's name or a prefix of its ID (`bdb bdbs` lists them)"
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"formulas"}, "Error: missing <bdb>: formulas takes <bdb>; " + bdbHint + "\n"},
		{[]string{"formulas", "show", "place-detail"}, `Error: missing <formula>: formulas show takes <bdb> ` +
			`<formula>; "place-detail" was read as <bdb>; <formula> is the formula's name (` +
			"`bdb formulas <bdb>` lists them)\n"},
		{[]string{"changes", "revert"}, "Error: missing <bdb>: changes revert takes <bdb> (<batch> | --entry <id>); " +
			bdbHint + "\n"},
		{[]string{"formulas", "Places", "list"}, "Error: \"list\" is a subcommand: put it first, " +
			"`bdb formulas list <bdb>`\n"},
		{[]string{"formulas", "list", "a", "b"}, "Error: too many arguments: formulas list takes <bdb>, got 2\n"},
		{[]string{"formulas", "shwo", "Places", "x"}, "Error: too many arguments: formulas takes <bdb>, got 3\n\n" +
			"Did you mean this?\n\tshow\n"},
		{[]string{"version", "x"}, "Error: too many arguments: version takes no arguments, got 1\n"},
		{[]string{"bdbs", "lsit"}, "Error: unknown command \"lsit\" for \"bdb bdbs\"\n\n" +
			"Did you mean this?\n\tlist\n"},
	} {
		code, _, stderr := runCLI(t, append([]string{"bdb"}, tt.args...)...)
		if code != exitUsage || !strings.HasPrefix(stderr, tt.want) {
			t.Errorf("bdb %v = %d, stderr %q; want %d and %q", tt.args, code, stderr, exitUsage, tt.want)
		}
	}

	if code, stdout, _ := runCLI(t, "bdb", "formulas", "--help"); code != exitOK ||
		!strings.Contains(stdout, "\n  bdb formulas <bdb> [flags]\n") {
		t.Errorf("formulas --help = %d %q, want the usage line with <bdb>", code, stdout)
	}
}

// TestRun_NoArgsPrintsHelp checks that the bare command prints its help and succeeds.
func TestRun_NoArgsPrintsHelp(t *testing.T) {
	code, stdout, _ := runCLI(t, "bdb")
	if code != exitOK || !strings.Contains(stdout, "Available Commands:") {
		t.Errorf("bdb = %d %q, want %d and the help text", code, stdout, exitOK)
	}
}

// TestRun_DataDirExpandsHome checks that a data directory starting with ~/, from the flag or from BAGUETTEDB_DATA_DIR
// as an MCP client's configuration passes it unexpanded, lives under the home directory, and that no directory named
// ~ appears in the working directory.
func TestRun_DataDirExpandsHome(t *testing.T) {
	clearDataDirEnv(t)
	home, work := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(work)

	if code, _, stderr := runCLI(t, "bdb", "--data-dir=~/from-flag", "bdbs"); code != exitOK {
		t.Fatalf("bdbs with --data-dir=~/from-flag = %d, stderr %q", code, stderr)
	}
	t.Setenv("BAGUETTEDB_DATA_DIR", "~/from-env")
	if code, _, stderr := runCLI(t, "bdb", "bdbs"); code != exitOK {
		t.Fatalf("bdbs with BAGUETTEDB_DATA_DIR=~/from-env = %d, stderr %q", code, stderr)
	}

	for _, dir := range []string{"from-flag", "from-env"} {
		if _, err := os.Stat(filepath.Join(home, dir, "instance.sqlite")); err != nil {
			t.Errorf("no instance in ~/%s: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "~")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat ./~ = %v; want no such directory", err)
	}
}

// TestRun_FirstUseKEK checks that the first command on a data directory says, in a plain line rather than a log
// line, that it created the key the bdbs are encrypted with and that it needs backing up, and that later ones don't.
func TestRun_FirstUseKEK(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")

	_, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs")
	want := "Created " + filepath.Join(dataDir, "kek") + ", the key your bdbs are encrypted with: back it up"
	if !strings.HasPrefix(stderr, want) || strings.Contains(stderr, "WRN") {
		t.Errorf("first bdbs, stderr %q; want it to start with %q, and no log line", stderr, want)
	}
	if _, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs"); strings.Contains(stderr, "Created ") {
		t.Errorf("second bdbs, stderr %q; want no word of the key", stderr)
	}
}
