//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/cli"
)

// servingURL matches the base URL bdb serve announces on stderr.
var servingURL = regexp.MustCompile(`on (http://127\.0\.0\.1:\d+) `)

// TestRun_Serve runs bdb serve on a free loopback port and checks that a personal access token created with tokens
// create authenticates on the API and over streamable MCP, as the owner, whose instructions say the data lives on the
// serving machine;
// that mcp config --http prints this server's endpoint, and mcp config points at --http while it runs;
// that a request with no token is refused, and that /mcp's refusals say how to get a token, the challenge doing so
// only for a token sent, as RFC 6750 wants;
// that a second bdb serve on the same data directory is refused, the first one's record kept;
// that a Host naming the server by an unknown name is refused, an --allow-host one is not;
// that the web app's configuration says it signs users in with tokens, and names the owner;
// that a page loaded through localhost is sent to the announced 127.0.0.1 URL, where the browser's cookie lives;
// and that cancelling the run stops the server with exit status 0, removing its record from the data directory.
func TestRun_Serve(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "tokens", "create", "serve-test")
	token := strings.TrimSuffix(stdout, "\n")
	if code != exitOK || !strings.HasPrefix(token, "bdbp_") {
		t.Fatalf("tokens create = %d %q, stderr %q", code, stdout, stderr)
	}

	baseURL, _, stop := startServe(t, dataDir, "--allow-host", "nas.local")

	get := func(path, host, bearer string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+path, nil)
		if err != nil {
			t.Fatalf("building GET %s: %v", path, err)
		}
		if host != "" {
			req.Host = host
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("closing GET %s: %v", path, err)
		}
		return resp.StatusCode
	}
	for _, tc := range []struct {
		name, host, bearer string
		want               int
	}{
		{"the token", "", token, http.StatusOK},
		{"no token", "", "", http.StatusUnauthorized},
		{"an unknown token", "", "bdbp_unknown", http.StatusUnauthorized},
		{"an unknown Host", "evil.example", token, http.StatusMisdirectedRequest},
		{"an allowed Host", "nas.local:3035", token, http.StatusOK},
		{"localhost", "localhost", token, http.StatusOK},
	} {
		if status := get("/api/bdbs/v1/bdbs", tc.host, tc.bearer); status != tc.want {
			t.Errorf("GET /api/bdbs/v1/bdbs with %s = %d, want %d", tc.name, status, tc.want)
		}
	}

	if code, stdout, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "mcp", "config", "--client", "claude",
		"--http"); code != exitOK || !strings.Contains(stdout, "bdb-serve "+baseURL+"/mcp --header") {
		t.Errorf("mcp config --http = %d %q, stderr %q; want this server's endpoint", code, stdout, stderr)
	}
	if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "mcp", "config", "--client", "claude"); code !=
		exitOK || !strings.Contains(stderr, "at "+baseURL+": `bdb mcp config --client claude --http` prints") {
		t.Errorf("mcp config = %d, stderr %q; want the pointer at --http", code, stderr)
	}

	hint := "create a personal access token with `bdb tokens create <token-name>` on the machine running bdb serve, " +
		"and send it as an `Authorization: Bearer` header"
	for _, tc := range []struct{ name, bearer, challenge, body string }{
		{"no token", "", "Bearer", "missing or invalid authorization header: " + hint},
		{"an unknown token", "bdbp_unknown", `Bearer error="invalid_token", error_description="` + hint + `"`,
			"invalid token: " + hint},
	} {
		if challenge, body := mcpRefusal(t, baseURL, tc.bearer); challenge != tc.challenge || body != tc.body {
			t.Errorf("POST /mcp with %s: WWW-Authenticate %q, error %q; want %q and %q", tc.name, challenge, body,
				tc.challenge, tc.body)
		}
	}

	second, cancelSecond := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelSecond()
	var secondErr syncBuffer
	code = cli.RunContext(second, []string{"bdb", "--data-dir", dataDir, "serve", "--listen", "127.0.0.1:0"},
		strings.NewReader(""), io.Discard, &secondErr)
	if want := "bdb serve is already serving " + dataDir + " at " + baseURL; code != exitFailure ||
		!strings.Contains(secondErr.String(), want) {
		t.Errorf("a second serve on the data directory = %d, stderr %q; want %d and %q", code, secondErr.String(),
			exitFailure, want)
	}
	if record, err := os.ReadFile(filepath.Join(dataDir, "serve.json")); err != nil ||
		!strings.Contains(string(record), baseURL) {
		t.Errorf("serve.json after the second serve = %q, %v; want the first server's record", record, err)
	}

	if mode, owner := webConfig(t, baseURL); mode != "token" || owner != "baker@bdb.localhost" {
		t.Errorf("/config.json authMode %q, ownerEmail %q; want token and baker@bdb.localhost", mode, owner)
	}
	if status, location := pageThroughLocalhost(t, baseURL, "/bdbs?tab=1"); status != http.StatusTemporaryRedirect ||
		location != baseURL+"/bdbs?tab=1" {
		t.Errorf("GET /bdbs through localhost = %d to %q, want 307 to %s/bdbs?tab=1", status, location, baseURL)
	}

	c, initResult := servedMCP(t, baseURL, token)
	if !strings.Contains(initResult.Instructions, "served over HTTP by bdb serve") ||
		!strings.Contains(initResult.Instructions, dataDir+" on the machine serving it") {
		t.Errorf("instructions = %q, want the served instance and its data directory on the serving machine",
			initResult.Instructions)
	}
	created := callToolJSON[map[string]any](t, c, "create_bdb", map[string]any{"name": "Served notes"})
	if created["name"] != "Served notes" {
		t.Errorf("create_bdb = %v, want the bdb Served notes", created)
	}
	if err := c.Close(); err != nil {
		t.Errorf("closing the MCP client: %v", err)
	}

	stop()
	if _, err := os.Stat(filepath.Join(dataDir, "serve.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("serve.json after the stop: %v, want it removed", err)
	}
	if code, stdout, _ := runCLI(t, "bdb", "--data-dir", dataDir, "bdbs"); code != exitOK ||
		!strings.Contains(stdout, "Served notes") || !strings.Contains(stdout, "owner") {
		t.Errorf("bdbs after serving = %d %q, want Served notes, owned by the owner", code, stdout)
	}
}

// servedMCP returns an MCP client initialized over streamable HTTP with the server at baseURL, token as its bearer
// token, with the initialize result. The caller closes the client.
func servedMCP(t *testing.T, baseURL, token string) (*mcpclient.Client, *mcp.InitializeResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := mcpclient.NewStreamableHttpClient(baseURL+"/mcp",
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatalf("creating the MCP client: %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("starting the MCP client: %v", err)
	}
	initResult, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "serve-test", Version: "1.0.0"},
	}})
	if err != nil {
		t.Fatalf("initializing over MCP with the token: %v", err)
	}
	return c, initResult
}

// mcpRefusal posts an MCP initialize request to the server at baseURL with bearer as its token (none when empty) and
// returns the WWW-Authenticate challenge and the error of the 401 it expects in answer, whose body must keep < as
// typed.
func mcpRefusal(t *testing.T, baseURL, bearer string) (challenge, errMsg string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("building POST /mcp: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing POST /mcp: %v", err)
		}
	}()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading POST /mcp: %v", err)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /mcp = %d, decoding: %v; want a 401 with a JSON error", resp.StatusCode, err)
	}
	if strings.Contains(string(raw), `\u003c`) {
		t.Errorf("POST /mcp body %s; want <token-name> as typed, not escaped", raw)
	}
	return resp.Header.Get("WWW-Authenticate"), body.Error
}

// startServe runs `bdb --data-dir dataDir serve --listen 127.0.0.1:0` with extra args until the test stops it, and
// returns the base URL it announces and the lines it starts with. stop cancels the run and fails the test unless it
// then exits 0.
func startServe(t *testing.T, dataDir string, extra ...string) (baseURL, banner string, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	// Stops a run the test left serving after failing halfway.
	t.Cleanup(cancel)
	var stdout, stderr syncBuffer
	exited := make(chan int, 1)
	args := append([]string{"bdb", "--data-dir", dataDir, "serve", "--listen", "127.0.0.1:0"}, extra...)
	go func() {
		exited <- cli.RunContext(ctx, args, strings.NewReader(""), &stdout, &stderr)
	}()

	stop = func() {
		cancel()
		select {
		case code := <-exited:
			if code != exitOK {
				t.Errorf("bdb serve exited %d after its context was cancelled; stderr:\n%s", code, stderr.String())
			}
		case <-time.After(15 * time.Second):
			t.Errorf("bdb serve did not stop after its context was cancelled; stderr:\n%s", stderr.String())
		}
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out := stderr.String()
		if m := servingURL.FindStringSubmatch(out); m != nil && strings.Contains(out, "tokens create") {
			return m[1], out, stop
		}
		select {
		case code := <-exited:
			t.Fatalf("bdb serve exited %d before serving; stderr:\n%s", code, stderr.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	stop()
	t.Fatalf("bdb serve announced no URL; stderr:\n%s", stderr.String())
	return "", "", nil
}

// TestRun_ServeSignIn checks password sign-in over bdb serve: the owner, given a password by init, and bob, created
// with --password-stdin, each sign in with a session cookie that authenticates as them; set-password replaces bob's
// password, the old one then refused; an account without a password can't sign in. The start lines say where and as
// whom the owner signs in, or, built without the web app (as when the checkout didn't sync its bundle), how to build
// one with it.
//
// It also checks that a new password ends the account's web sessions and says so, from set-password and init alike,
// unless --keep-sessions keeps them, and leaves other accounts' sessions working.
func TestRun_ServeSignIn(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	bdb := func(stdin string, args ...string) string {
		t.Helper()
		code, _, stderr := runCLIWithStdin(t, stdin, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
		if code != exitOK {
			t.Fatalf("%v = %d, stderr %q", args, code, stderr)
		}
		return stderr
	}
	bdb("ada@example.com\nAda\ncorrect-horse\ncorrect-horse\n", "init")
	bdb("bob-password-1\n", "users", "create", "bob@example.com", "--password-stdin")
	bdb("", "users", "create", "carol@example.com")

	baseURL, banner, stop := startServe(t, dataDir)
	defer stop()
	if !strings.Contains(banner, "Sign in at "+baseURL+" as ada@example.com, with your password.") &&
		!strings.Contains(banner, "This build has no web app") {
		t.Errorf("start lines %q, want where the owner signs in, or the note on building with the web app", banner)
	}

	sessions := map[string]*http.Cookie{}
	passwords := map[string]string{"ada@example.com": "correct-horse", "bob@example.com": "bob-password-1"}
	for email, password := range passwords {
		status, cookie := signIn(t, baseURL, email, password)
		if status != http.StatusNoContent || cookie == nil {
			t.Fatalf("signing in as %s = %d with cookie %v, want 204 and a session cookie", email, status, cookie)
		}
		if me := whoAmI(t, baseURL, cookie); me != email {
			t.Errorf("GET /me with %s's session = %q, want %s", email, me, email)
		}
		sessions[email] = cookie
	}

	stderr := bdb("bob-password-2\n", "users", "set-password", "bob@example.com", "--password-stdin", "--keep-sessions")
	if !strings.Contains(stderr, "Set the password of bob@example.com.\n") || whoAmI(t, baseURL,
		sessions["bob@example.com"]) == "" {
		t.Errorf("set-password --keep-sessions, stderr %q; want the password set and bob's session kept", stderr)
	}
	for _, tc := range []struct {
		email, password string
		want            int
	}{
		{"bob@example.com", "bob-password-1", http.StatusUnauthorized},
		{"bob@example.com", "bob-password-2", http.StatusNoContent},
		{"carol@example.com", "", http.StatusUnauthorized},
	} {
		if status, _ := signIn(t, baseURL, tc.email, tc.password); status != tc.want {
			t.Errorf("signing in as %s with %q = %d, want %d", tc.email, tc.password, status, tc.want)
		}
	}

	stderr = bdb("bob-password-3\n", "users", "set-password", "bob@example.com", "--password-stdin")
	if !strings.Contains(stderr, "Set the password of bob@example.com, and ended its web sessions.") ||
		whoAmI(t, baseURL, sessions["bob@example.com"]) != "" ||
		whoAmI(t, baseURL, sessions["ada@example.com"]) != "ada@example.com" {
		t.Errorf("set-password, stderr %q; want bob's session ended, saying so, and ada's kept", stderr)
	}

	stderr = bdb("\n\nbattery-staple\nbattery-staple\n", "init")
	if !strings.Contains(stderr, "Ended your web sessions: sign in again with the new password.") ||
		whoAmI(t, baseURL, sessions["ada@example.com"]) != "" {
		t.Errorf("init with a new password, stderr %q; want ada's session ended, saying so", stderr)
	}
}

// TestRun_ServeSessionsPerDataDir runs two bdb serve on one host name, each on its own data directory, and signs a
// cookie jar in to both, as a browser holding both sessions: cookies ignore ports, so each server names its session
// cookie after its data directory, and both sessions keep working side by side.
func TestRun_ServeSessionsPerDataDir(t *testing.T) {
	clearDataDirEnv(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	var baseURLs, names []string
	for _, dir := range []string{"one", "two"} {
		dataDir := filepath.Join(t.TempDir(), dir)
		if code, _, stderr := runCLIWithStdin(t, "\n\ncorrect-horse\ncorrect-horse\n", "bdb", "--data-dir", dataDir,
			"init"); code != exitOK {
			t.Fatalf("init on %s = %d, stderr %q", dir, code, stderr)
		}
		baseURL, _, stop := startServe(t, dataDir)
		defer stop()
		status, cookie := signIn(t, baseURL, "baker@bdb.localhost", "correct-horse")
		if status != http.StatusNoContent || cookie == nil {
			t.Fatalf("signing in on %s = %d with cookie %v, want 204 and a bdb_session_ cookie", dir, status, cookie)
		}
		u, err := url.Parse(baseURL)
		if err != nil {
			t.Fatal(err)
		}
		jar.SetCookies(u, []*http.Cookie{cookie})
		baseURLs, names = append(baseURLs, baseURL), append(names, cookie.Name)
	}

	if names[0] == names[1] {
		t.Errorf("both servers name their session cookie %s, want one name per data directory", names[0])
	}
	for _, baseURL := range baseURLs {
		resp, err := client.Get(baseURL + "/api/auth/v1/me")
		if err != nil {
			t.Fatalf("GET %s/api/auth/v1/me: %v", baseURL, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s/api/auth/v1/me with both sessions in the jar = %d, want 200", baseURL, resp.StatusCode)
		}
	}
}

// TestRun_ServeTokensFromTheWebApp checks that an account other than the owner manages its own personal access
// tokens from the web app over bdb serve, with its session alone: it creates one, with an expiry, which authenticates
// as it and which tokens list --as shows on the serving machine; lists it; and revokes it, after which it stops
// authenticating.
func TestRun_ServeTokensFromTheWebApp(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	for _, step := range []struct {
		stdin string
		args  []string
	}{
		{"ada@example.com\nAda\ncorrect-horse\ncorrect-horse\n", []string{"init"}},
		{"bob-password-1\n", []string{"users", "create", "bob@example.com", "--password-stdin"}},
	} {
		args := append([]string{"bdb", "--data-dir", dataDir}, step.args...)
		if code, _, stderr := runCLIWithStdin(t, step.stdin, args...); code != exitOK {
			t.Fatalf("%v = %d, stderr %q", step.args, code, stderr)
		}
	}
	baseURL, _, stop := startServe(t, dataDir)
	defer stop()
	status, session := signIn(t, baseURL, "bob@example.com", "bob-password-1")
	if status != http.StatusNoContent || session == nil {
		t.Fatalf("signing in as bob = %d with cookie %v, want 204 and a session cookie", status, session)
	}

	expiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	status, body := withSession(t, http.MethodPost, baseURL+"/api/auth/v1/personal-tokens", session,
		map[string]string{"name": "laptop", "expires_at": expiresAt})
	var created struct {
		ID        string  `json:"id"`
		Token     string  `json:"token"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &created); status != http.StatusCreated || err != nil ||
		!strings.HasPrefix(created.Token, "bdbp_") || created.ExpiresAt == nil {
		t.Fatalf("POST /personal-tokens as bob = %d %s (%v); want 201, a token and its expiry", status, body, err)
	}
	if code, stdout, _ := runCLI(t, "bdb", "--data-dir", dataDir, "--as", "bob@example.com", "tokens"); code !=
		exitOK || !strings.Contains(stdout, created.ID) {
		t.Errorf("tokens --as bob = %d %q, want the token created from the web app", code, stdout)
	}
	if me := whoAmIWithToken(t, baseURL, created.Token); me != "bob@example.com" {
		t.Errorf("GET /me with the new token = %q, want bob@example.com", me)
	}

	status, body = withSession(t, http.MethodGet, baseURL+"/api/auth/v1/personal-tokens", session, nil)
	if status != http.StatusOK || !strings.Contains(string(body), created.ID) {
		t.Errorf("GET /personal-tokens as bob = %d %s, want the new token", status, body)
	}
	status, body = withSession(t, http.MethodDelete, baseURL+"/api/auth/v1/personal-tokens/"+created.ID, session, nil)
	if status != http.StatusNoContent || whoAmIWithToken(t, baseURL, created.Token) != "" {
		t.Errorf("DELETE /personal-tokens/{id} as bob = %d %s; want 204 and the token refused after", status, body)
	}
}

// withSession sends method to target with the session in cookie and body as JSON (none when nil), and returns the
// status and the response body.
func withSession(t *testing.T, method, target string, cookie *http.Cookie, body any) (int, []byte) {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reqBody = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, target, reqBody)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, target, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing %s %s: %v", method, target, err)
		}
	}()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s %s: %v", method, target, err)
	}
	return resp.StatusCode, raw
}

// whoAmIWithToken returns the email GET /api/auth/v1/me answers at baseURL for the bearer token, empty when it refuses.
func whoAmIWithToken(t *testing.T, baseURL, token string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/auth/v1/me", nil)
	if err != nil {
		t.Fatalf("building GET /me: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing GET /me: %v", err)
		}
	}()
	var me struct {
		Email string `json:"email"`
	}
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatalf("decoding GET /me: %v", err)
	}
	return me.Email
}

// signIn posts email and password to the password sign-in of the server at baseURL and returns the status and the
// session cookie the answer sets, nil when none.
func signIn(t *testing.T, baseURL, email, password string) (int, *http.Cookie) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/api/auth/v1/session",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building the sign-in: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("signing in: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the sign-in: %v", err)
	}
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "bdb_session_") {
			return resp.StatusCode, c
		}
	}
	return resp.StatusCode, nil
}

// whoAmI returns the email GET /api/auth/v1/me answers at baseURL for the session in cookie, empty when it refuses.
func whoAmI(t *testing.T, baseURL string, cookie *http.Cookie) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/auth/v1/me", nil)
	if err != nil {
		t.Fatalf("building GET /me: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing GET /me: %v", err)
		}
	}()
	var me struct {
		Email string `json:"email"`
	}
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatalf("decoding GET /me: %v", err)
	}
	return me.Email
}

// webConfig returns the authMode and the ownerEmail the web app's runtime configuration at baseURL publishes.
func webConfig(t *testing.T, baseURL string) (authMode, ownerEmail string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/config.json", nil)
	if err != nil {
		t.Fatalf("building GET /config.json: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /config.json: %v", err)
	}
	var cfg struct {
		AuthMode   string `json:"authMode"`
		OwnerEmail string `json:"ownerEmail"`
	}
	err = json.NewDecoder(resp.Body).Decode(&cfg)
	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Fatalf("closing GET /config.json: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("decoding /config.json: %v", err)
	}
	return cfg.AuthMode, cfg.OwnerEmail
}

// pageThroughLocalhost loads path from the server at baseURL, a 127.0.0.1 URL, naming it localhost instead, without
// following redirects, and returns the status and the Location header.
func pageThroughLocalhost(t *testing.T, baseURL, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+path, nil)
	if err != nil {
		t.Fatalf("building GET %s: %v", path, err)
	}
	req.Host = strings.Replace(strings.TrimPrefix(baseURL, "http://"), "127.0.0.1", "localhost", 1)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing GET %s: %v", path, err)
	}
	return resp.StatusCode, resp.Header.Get("Location")
}
