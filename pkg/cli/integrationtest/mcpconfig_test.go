//go:build integration

package integrationtest

import (
	"strings"
	"testing"
)

// TestRun_MCPConfig checks the mcp config command end to end: the snippet names the binary as run and adds
// --data-dir only when one was chosen, and a missing or unknown --client is a usage error listing the clients.
//
// It also checks the other forms: --name names the entry, --url prints the HTTP form for that server, under
// bdb-serve, and --http without a running bdb serve says how to start one. --http with --url, a name that isn't one,
// and a URL that isn't http or https are usage errors.
func TestRun_MCPConfig(t *testing.T) {
	clearDataDirEnv(t)
	t.Setenv("HOME", t.TempDir())

	code, stdout, stderr := runCLI(t, "/opt/tools/bdb", "mcp", "config", "--client", "codex")
	if code != exitOK || !strings.Contains(stdout, "codex mcp add bdb-local -- /opt/tools/bdb mcp\n") {
		t.Errorf("default data dir = %d %q (stderr %q), want the plain command", code, stdout, stderr)
	}

	code, stdout, _ = runCLI(t, "/opt/tools/bdb", "--data-dir", "/srv/bdb", "mcp", "config", "--client", "claude")
	if code != exitOK || !strings.Contains(stdout, "-- /opt/tools/bdb mcp --data-dir /srv/bdb\n") {
		t.Errorf("chosen data dir = %d %q, want --data-dir in the command", code, stdout)
	}

	code, stdout, _ = runCLI(t, "/opt/tools/bdb", "mcp", "config", "--client", "codex", "--name", "bdb-alice")
	if code != exitOK || !strings.Contains(stdout, "codex mcp add bdb-alice -- /opt/tools/bdb mcp\n") {
		t.Errorf("--name = %d %q, want the entry named bdb-alice", code, stdout)
	}

	code, stdout, stderr = runCLI(t, "bdb", "mcp", "config", "--client", "codex", "--url", "http://nas.local:3035")
	if want := "[mcp_servers.bdb-serve]\nurl = \"http://nas.local:3035/mcp\"\n" +
		"http_headers = { Authorization = \"Bearer <token>\" }\n"; code != exitOK || !strings.Contains(stdout, want) {
		t.Errorf("--url = %d %q (stderr %q), want %q", code, stdout, stderr, want)
	}

	if code, _, stderr = runCLI(t, "bdb", "mcp", "config", "--client", "claude", "--http"); code != exitFailure ||
		!strings.Contains(stderr, "no bdb serve is running on ") || !strings.Contains(stderr, "`bdb serve`") {
		t.Errorf("--http with no server = %d, stderr %q; want 1 and how to start one", code, stderr)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--http", "--url", "http://nas.local:3035"}, "--http and --url both say where the server is"},
		{[]string{"--name", "bdb local"}, `--name "bdb local": want 1 to 64 letters, digits, - or _`},
		{[]string{"--url", "nas.local:3035"}, `--url "nas.local:3035": want the http or https URL`},
	} {
		args := append([]string{"bdb", "mcp", "config", "--client", "claude"}, tc.args...)
		if code, _, stderr := runCLI(t, args...); code != exitUsage || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v = %d, stderr %q; want %d and %q", tc.args, code, stderr, exitUsage, tc.want)
		}
	}

	for _, args := range [][]string{{"bdb", "mcp", "config"}, {"bdb", "mcp", "config", "--client", "cursor"}} {
		code, _, stderr := runCLI(t, args...)
		if code != exitUsage || !strings.Contains(stderr, "  claude          Claude Desktop and Claude Code\n") {
			t.Errorf("%v = %d, stderr %q; want %d and the list of clients", args, code, stderr, exitUsage)
		}
	}
}
