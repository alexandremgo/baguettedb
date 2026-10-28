//go:build integration

package integrationtest

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRun_Tokens checks the tokens commands: tokens create prints a token's value alone on stdout, for the owner by
// default and for another account with --as; tokens list shows each account its own tokens, by the start of their
// value and never used yet, as a table and as JSON; tokens revoke revokes a token, named by a prefix of its ID, of the
// account --as names only, pointing at --as otherwise, and names it, after which that account lists none. A missing
// token name, an unknown account, an ID matching no token and a lifetime that isn't a positive duration are refused.
//
// A token created with --expires-in says when it expires, and lists it, as a table and as JSON; the others never do.
func TestRun_Tokens(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	bdb := func(args ...string) (int, string, string) {
		t.Helper()
		return runCLI(t, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
	}
	if code, _, stderr := bdb("users", "create", "bob@example.com"); code != exitOK {
		t.Fatalf("users create = %d, stderr %q", code, stderr)
	}

	code, stdout, stderr := bdb("tokens", "create", "laptop")
	ownerToken := strings.TrimSuffix(stdout, "\n")
	if code != exitOK || !strings.HasPrefix(ownerToken, "bdbp_") || strings.ContainsAny(ownerToken, " \n") ||
		!strings.Contains(stderr, "for baker@bdb.localhost. Copy it now") {
		t.Fatalf("tokens create = %d %q, stderr %q; want the owner's token alone on stdout", code, stdout, stderr)
	}
	if code, stdout, stderr = bdb("--as", "bob@example.com", "tokens", "create", "ci"); code != exitOK ||
		!strings.HasPrefix(stdout, "bdbp_") || !strings.Contains(stderr, "for bob@example.com.") {
		t.Fatalf("tokens create --as = %d %q, stderr %q; want bob's token", code, stdout, stderr)
	}

	code, stdout, stderr = bdb("tokens")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != exitOK || len(lines) != 2 ||
		strings.Join(strings.Fields(lines[0]), " ") != "ID NAME TOKEN CREATED LAST USED EXPIRES" {
		t.Fatalf("tokens = %d %q, stderr %q; want a header and the owner's one token", code, stdout, stderr)
	}
	if fields := strings.Fields(lines[1]); len(fields) != 6 || fields[1] != "laptop" ||
		fields[2] != ownerToken[:len("bdbp_")+8]+"…" || fields[4] != "never" || fields[5] != "never" {
		t.Errorf("tokens row = %q, want laptop, the start of its value, never used and never expiring", lines[1])
	}

	code, stdout, _ = bdb("--as", "bob@example.com", "tokens", "list", "--json")
	var tokens []struct {
		ID         string  `json:"id"`
		Name       string  `json:"name"`
		Prefix     string  `json:"prefix"`
		LastUsedAt *string `json:"last_used_at"`
	}
	if err := json.Unmarshal([]byte(stdout), &tokens); code != exitOK || err != nil || len(tokens) != 1 ||
		tokens[0].Name != "ci" || !strings.HasPrefix(tokens[0].Prefix, "bdbp_") || tokens[0].LastUsedAt != nil {
		t.Fatalf("tokens list --as --json = %d %q (%v), want bob's ci token, never used", code, stdout, err)
	}

	want := "no token with an ID starting with \"" + tokens[0].ID + "\" for baker@bdb.localhost; `bdb tokens` lists " +
		"them; another account's token takes --as <email>"
	if code, _, stderr = bdb("tokens", "revoke", tokens[0].ID); code != exitFailure || !strings.Contains(stderr, want) {
		t.Errorf("tokens revoke of bob's token as the owner = %d, stderr %q; want 1 and %q", code, stderr, want)
	}
	if code, stdout, stderr = bdb("--as", "bob@example.com", "tokens", "revoke",
		strings.ToUpper(tokens[0].ID[:8])); code != exitOK || stdout != "Revoked token ci ("+tokens[0].ID+")\n" {
		t.Fatalf("tokens revoke of a prefix --as = %d %q, stderr %q; want ci revoked", code, stdout, stderr)
	}
	if code, stdout, stderr = bdb("--as", "bob@example.com", "tokens"); code != exitOK || stdout != "" ||
		!strings.Contains(stderr, "`bdb --as bob@example.com tokens create <token-name>`") {
		t.Errorf("tokens --as after the revoke = %d %q, stderr %q; want none and the create hint", code, stdout,
			stderr)
	}

	if code, _, stderr = bdb("tokens", "create", "monthly", "--expires-in", "30d"); code != exitOK ||
		!strings.Contains(stderr, ", expiring "+time.Now().Add(30*24*time.Hour).Format(time.DateOnly)) {
		t.Errorf("tokens create --expires-in 30d = %d, stderr %q; want it expiring in 30 days", code, stderr)
	}
	code, stdout, _ = bdb("tokens", "--json")
	var owned []struct {
		Name      string  `json:"name"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(stdout), &owned); code != exitOK || err != nil || len(owned) != 2 ||
		owned[0].Name != "monthly" || owned[0].ExpiresAt == nil || owned[1].ExpiresAt != nil {
		t.Errorf("tokens --json = %d %q (%v); want monthly with its expiry, then laptop without", code, stdout, err)
	}

	for _, tc := range []struct {
		args     []string
		code     int
		inStderr string
	}{
		{[]string{"tokens", "create"}, exitUsage, "missing <token-name>"},
		{[]string{"--as", "nobody@example.com", "tokens", "create", "x"}, exitFailure, "`bdb users`"},
		{[]string{"tokens", "revoke", "nope"}, exitFailure, `no token with an ID starting with "nope"`},
		{[]string{"tokens", "create", "x", "--expires-in", "0d"}, exitUsage,
			`--expires-in "0d": want a positive duration, such as 30d, 12h or 90m`},
		{[]string{"tokens", "create", "x", "--expires-in", "soon"}, exitUsage, `--expires-in "soon"`},
	} {
		if code, _, stderr := bdb(tc.args...); code != tc.code || !strings.Contains(stderr, tc.inStderr) ||
			strings.Contains(stderr, "%!") {
			t.Errorf("%v = %d, stderr %q; want %d mentioning %s", tc.args, code, stderr, tc.code, tc.inStderr)
		}
	}
}
