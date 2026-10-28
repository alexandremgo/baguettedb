//go:build integration

package integrationtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_As checks the global --as flag. Commands act as the account it names: its bdbs alone, owned by it, apart
// from the owner's; its tokens; an MCP client's calls through bdb mcp, whose instructions name the account. mcp config
// registers it under an entry of its own, passing --as on.
//
// An email no account has is refused, pointing at users list and users create; init and serve refuse --as.
func TestRun_As(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	bdb := func(args ...string) (int, string, string) {
		t.Helper()
		return runCLI(t, append([]string{"/opt/tools/bdb", "--data-dir", dataDir}, args...)...)
	}
	for _, args := range [][]string{
		{"bdbs", "create", "Owner notes"},
		{"users", "create", "alice.smith@example.com"},
		{"--as", "Alice.Smith@example.com", "bdbs", "create", "Alice notes"},
	} {
		if code, _, stderr := bdb(args...); code != exitOK {
			t.Fatalf("%v = %d, stderr %q", args, code, stderr)
		}
	}

	if code, stdout, _ := bdb("--as", "alice.smith@example.com", "bdbs"); code != exitOK ||
		!hasRowPrefix(strings.Split(stdout, "\n"), []string{"Alice", "notes"}) || strings.Contains(stdout, "Owner") ||
		!strings.Contains(stdout, "owner") {
		t.Errorf("bdbs --as alice = %d %q, want Alice notes alone, which she owns", code, stdout)
	}
	if _, stdout, _ := bdb("bdbs"); strings.Contains(stdout, "Alice") {
		t.Errorf("bdbs as the owner = %q, want none of Alice's bdbs", stdout)
	}

	code, _, stderr := bdb("--as", "alice.smith@example.com", "tokens", "create", "laptop")
	if code != exitOK || !strings.Contains(stderr, "for alice.smith@example.com.") {
		t.Errorf("tokens create --as alice = %d, stderr %q; want her token", code, stderr)
	}
	if _, stdout, _ := bdb("tokens"); strings.Contains(stdout, "laptop") {
		t.Errorf("tokens as the owner = %q, want none of Alice's", stdout)
	}

	c, initResult, stop := startMCP(t, dataDir, "--as", "alice.smith@example.com")
	if !strings.Contains(initResult.Instructions, "You act as alice.smith@example.com") {
		t.Errorf("instructions %q, want them to name the account acted as", initResult.Instructions)
	}
	callToolJSON[map[string]any](t, c, "create_bdb", map[string]any{"name": "Alice groceries"})
	stop()
	if _, stdout, _ := bdb("--as", "alice.smith@example.com", "bdbs"); !strings.Contains(stdout, "Alice groceries") {
		t.Errorf("bdbs --as alice after bdb mcp --as created one = %q, want it hers", stdout)
	}

	code, stdout, _ := bdb("--as", "alice.smith@example.com", "mcp", "config", "--client", "codex")
	if want := "codex mcp add bdb-local-alice-smith -- /opt/tools/bdb mcp --data-dir " + dataDir +
		" --as alice.smith@example.com\n"; code != exitOK || !strings.Contains(stdout, want) {
		t.Errorf("mcp config --as alice = %d %q, want %q", code, stdout, want)
	}

	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--as", "nobody@example.com", "bdbs"}, exitFailure, "--as: no user with email nobody@example.com; " +
			"`bdb users` lists the accounts, and `bdb users create <email>` creates one"},
		{[]string{"--as", "alice.smith@example.com", "init"}, exitUsage, "--as doesn't apply"},
		{[]string{"--as", "alice.smith@example.com", "serve"}, exitUsage, "--as doesn't apply"},
	} {
		if code, _, stderr := bdb(tc.args...); code != tc.code || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v = %d, stderr %q; want %d and %q", tc.args, code, stderr, tc.code, tc.want)
		}
	}
}
