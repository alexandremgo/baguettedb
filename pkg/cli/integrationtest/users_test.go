//go:build integration

package integrationtest

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_Users checks that users create creates an account, an admin with --admin, named after --name split into
// first and last names, and points at the token it signs in with; that users list prints every account, the owner
// included, as a table and as JSON; and that an email already in use, whatever its case, an invalid email, named
// without any internal prefix, and a missing email are refused.
func TestRun_Users(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	bdb := func(args ...string) (int, string, string) {
		t.Helper()
		return runCLI(t, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
	}

	code, stdout, stderr := bdb("users", "create", "bob@example.com", "--name", "Bob van Martin", "--admin")
	if code != exitOK || !strings.HasPrefix(stdout, "Created bob@example.com (") ||
		!strings.HasSuffix(stdout, "), an admin\n") ||
		!strings.Contains(stderr, "`bdb --as bob@example.com tokens create <token-name>`") {
		t.Fatalf("users create --admin = %d %q, stderr %q; want the admin created and the tokens create hint", code,
			stdout, stderr)
	}
	if code, stdout, stderr = bdb("users", "create", "carol@example.com"); code != exitOK ||
		strings.Contains(stdout, "admin") {
		t.Fatalf("users create = %d %q, stderr %q; want a plain user created", code, stdout, stderr)
	}

	code, stdout, stderr = bdb("users")
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if code != exitOK || len(lines) != 4 || strings.Join(strings.Fields(lines[0]), " ") != "EMAIL NAME ROLE CREATED" {
		t.Fatalf("users = %d %q, stderr %q; want a header and three accounts", code, stdout, stderr)
	}
	for _, want := range [][]string{
		{"bob@example.com", "Bob", "van", "Martin", "admin"},
		{"carol@example.com", "user"},
		{"baker@bdb.localhost", "Baker", "admin"},
	} {
		if !hasRowPrefix(lines[1:], want) {
			t.Errorf("users %q has no row starting %v", stdout, want)
		}
	}

	code, stdout, _ = bdb("users", "list", "--json")
	var users []struct {
		Email     string `json:"email"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Admin     bool   `json:"admin"`
	}
	if err := json.Unmarshal([]byte(stdout), &users); code != exitOK || err != nil || len(users) != 3 {
		t.Fatalf("users list --json = %d %q (%v), want the three accounts", code, stdout, err)
	}
	for _, u := range users {
		if u.Email == "bob@example.com" && (u.FirstName != "Bob" || u.LastName != "van Martin" || !u.Admin) {
			t.Errorf("bob in users list --json = %+v, want Bob / van Martin, an admin", u)
		}
	}

	if code, _, stderr = bdb("users", "create", "BOB@example.com"); code != exitFailure ||
		!strings.Contains(stderr, "a user already has that email") || !strings.Contains(stderr, "`bdb users`") {
		t.Errorf("users create of a taken email = %d, stderr %q; want 1 pointing at users", code, stderr)
	}
	if code, _, stderr = bdb("users", "create", "not-an-email"); code != exitFailure ||
		stderr != "Error: \"not-an-email\" is not an email address\n" {
		t.Errorf("users create of an invalid email = %d, stderr %q; want 1 and the email named", code, stderr)
	}
	if code, _, stderr = bdb("users", "create"); code != exitUsage || !strings.Contains(stderr, "missing <email>") ||
		strings.Contains(stderr, "%!") {
		t.Errorf("users create with no email = %d, stderr %q; want a usage error", code, stderr)
	}
}

// TestRun_UsersPasswords checks the passwords of users create and users set-password without a terminal: create
// --password-stdin gives the account a password, as its hint says, and refuses a weak or missing one; create without
// it makes an account with no password, pointing at tokens and set-password; set-password needs --password-stdin,
// refuses a weak password and an unknown account, and sets one otherwise.
func TestRun_UsersPasswords(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	bdb := func(stdin string, args ...string) (int, string, string) {
		t.Helper()
		return runCLIWithStdin(t, stdin, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
	}

	for _, tc := range []struct {
		name, stdin string
		args        []string
		code        int
		stderr      string
	}{
		{"create --password-stdin", "bob-password\n",
			[]string{"users", "create", "bob@example.com", "--password-stdin"},
			exitOK, "It signs in to `bdb serve`'s web app with this email and its password."},
		{"create --password-stdin, a weak password", "short\n",
			[]string{"users", "create", "dave@example.com", "--password-stdin"}, exitFailure, "too weak"},
		{"create --password-stdin, no password", "",
			[]string{"users", "create", "dave@example.com", "--password-stdin"},
			exitUsage, "--password-stdin read no password"},
		{"create without a password", "", []string{"users", "create", "carol@example.com"}, exitOK,
			"It has no password: create a token it signs in with, `bdb --as carol@example.com tokens create " +
				"<token-name>`, or give it a password, `bdb users set-password carol@example.com`."},
		{"set-password without a terminal", "", []string{"users", "set-password", "carol@example.com"}, exitUsage,
			"no terminal to ask the password in: pass it with --password-stdin"},
		{"set-password, a weak password", "short\n",
			[]string{"users", "set-password", "carol@example.com", "--password-stdin"}, exitFailure, "too weak"},
		{"set-password, an unknown account", "carol-password\n",
			[]string{"users", "set-password", "nobody@example.com", "--password-stdin"}, exitFailure, "`bdb users`"},
		{"set-password", "carol-password\n", []string{"users", "set-password", "carol@example.com", "--password-stdin"},
			exitOK, "Set the password of carol@example.com."},
	} {
		if code, _, stderr := bdb(tc.stdin, tc.args...); code != tc.code || !strings.Contains(stderr, tc.stderr) {
			t.Errorf("%s = %d, stderr %q; want %d and %q", tc.name, code, stderr, tc.code, tc.stderr)
		}
	}
	if code, stdout, _ := bdb("", "users"); code != exitOK || strings.Contains(stdout, "dave@") {
		t.Errorf("users = %d %q, want no account for the refused creations", code, stdout)
	}
}
