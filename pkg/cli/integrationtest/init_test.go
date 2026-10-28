//go:build integration

package integrationtest

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_Init checks bdb init on a data directory whose owner is still the default one, the answers piped in:
// a password missing, typed differently the second time or weak is asked again, and refused after three tries; an
// email another account has is refused at once; none of them changes anything; then the owner becomes Ada Lovelace,
// whose password is asked again after two failed tries, and keeps their bdb; a second init with --name and
// empty answers changes the name alone; commands without --as act as the renamed owner.
func TestRun_Init(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	bdb := func(stdin string, args ...string) (int, string, string) {
		t.Helper()
		return runCLIWithStdin(t, stdin, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
	}
	for _, args := range [][]string{{"bdbs", "create", "Notes"}, {"users", "create", "bob@example.com"}} {
		if code, _, stderr := bdb("", args...); code != exitOK {
			t.Fatalf("%v = %d, stderr %q", args, code, stderr)
		}
	}

	for _, tc := range []struct {
		name, stdin, want string
		retries           int
	}{
		{"no password", "\n\n\n", "a password is required", 2},
		{"two passwords that differ", "\n\n" + strings.Repeat("correct-horse\ncorrect-hose\n", 3),
			"the two passwords differ", 2},
		{"a weak password", "\n\n" + strings.Repeat("short\nshort\n", 3), "the password is too weak", 2},
		{"an email in use", "bob@example.com\n\ncorrect-horse\ncorrect-horse\n", "a user already has that email", 0},
	} {
		code, _, stderr := bdb(tc.stdin, "init")
		if code != exitFailure || !strings.Contains(stderr, tc.want) ||
			strings.Count(stderr, "Try again.") != tc.retries {
			t.Errorf("init with %s = %d, stderr %q; want %d, %q and %d retries", tc.name, code, stderr, exitFailure,
				tc.want, tc.retries)
		}
	}
	if code, stdout, _ := bdb("", "users"); code != exitOK || !strings.Contains(stdout, "baker@bdb.localhost") {
		t.Errorf("users after the refused inits = %d %q, want the owner unchanged", code, stdout)
	}
	if code, _, stderr := bdb("\n\n\n", "init"); code != exitFailure ||
		!strings.Contains(stderr, "a password is required") {
		t.Errorf("init after the refused inits = %d, stderr %q; want the owner still without a password", code, stderr)
	}

	code, _, stderr := bdb("ada@example.com\nAda Lovelace\n\ncorrect-horse\ncorrect-hose\ncorrect-horse\n"+
		"correct-horse\n", "init")
	if want := "You are Ada Lovelace <ada@example.com>, the owner"; code != exitOK || !strings.Contains(stderr, want) ||
		!strings.Contains(stderr, "A password is required. Try again.") ||
		!strings.Contains(stderr, "The two passwords differ. Try again.") {
		t.Fatalf("init = %d, stderr %q; want %q after two retries", code, stderr, want)
	}
	code, stdout, _ := bdb("", "users")
	if code != exitOK || !strings.Contains(stdout, "ada@example.com") || strings.Contains(stdout, "baker@") {
		t.Errorf("users after init = %d %q, want ada@example.com in place of the default owner", code, stdout)
	}
	if code, stdout, _ := bdb("", "bdbs"); code != exitOK || !strings.Contains(stdout, "Notes") ||
		!strings.Contains(stdout, "owner") {
		t.Errorf("bdbs after init = %d %q, want Notes, still owned", code, stdout)
	}

	if code, _, stderr := bdb("\n\n", "init", "--name", "Ada King"); code != exitOK ||
		!strings.Contains(stderr, "You are Ada King <ada@example.com>") {
		t.Errorf("init --name with empty answers = %d, stderr %q; want the name changed alone", code, stderr)
	}
	if code, _, stderr := bdb("", "tokens", "create", "laptop"); code != exitOK ||
		!strings.Contains(stderr, "for ada@example.com") {
		t.Errorf("tokens create without --as = %d, stderr %q; want a token for the renamed owner", code, stderr)
	}
}

// TestRun_ServeOwnerPassword checks that bdb serve, started without a terminal, tells an owner without a password to
// run init, and that once init set one, the owner signs in to the web app with their email and password, while a
// wrong password is refused.
func TestRun_ServeOwnerPassword(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")

	_, banner, stop := startServe(t, dataDir)
	stop()
	want := "The owner, baker@bdb.localhost, has no password yet to sign in to the web app with: run `bdb init`"
	if !strings.Contains(banner, want) {
		t.Errorf("start lines %q, want %q", banner, want)
	}

	if code, _, stderr := runCLIWithStdin(t, "ada@example.com\nAda\ncorrect-horse\ncorrect-horse\n",
		"bdb", "--data-dir", dataDir, "init"); code != exitOK {
		t.Fatalf("init = %d, stderr %q", code, stderr)
	}
	baseURL, banner, stop := startServe(t, dataDir)
	defer stop()
	if strings.Contains(banner, "no password") {
		t.Errorf("start lines %q, want no password hint once init set one", banner)
	}
	for password, want := range map[string]int{"correct-horse": http.StatusNoContent,
		"wrong": http.StatusUnauthorized} {
		if status, _ := signIn(t, baseURL, "ada@example.com", password); status != want {
			t.Errorf("signing in with %q = %d, want %d", password, status, want)
		}
	}
}
