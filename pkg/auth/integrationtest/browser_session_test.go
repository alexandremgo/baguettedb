//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	authmodule "github.com/alexandremgo/baguettedb/pkg/auth"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// sessionCookie is the cookie a signed-in browser carries its token in.
const sessionCookie = "bdb_access_token"

// TestBrowserSession_Password checks the password sign-in of an auth app with no OAuth flow: the right email and
// password set the 30-day session cookie, which authenticates the browser, and so does the session sent as a bearer
// token, as an app would, while it is no personal token; a wrong password, an unknown email and an account without a
// password get the same 401 and no cookie; a body not sent as JSON is 415; signing out ends the session, cookie and
// bearer alike, and signing out without a cookie does nothing.
func TestBrowserSession_Password(t *testing.T) {
	truncateAll(t)
	res := setupPersonalTokensOnly(t)
	ctx := context.Background()
	userID := createSessionUser(t, res, "password@example.com")
	if _, err := res.UserSvc.CreateAccount(ctx, "nopassword@example.com", "No", "Password", "", false); err !=
		nil {
		t.Fatalf("creating an account without a password: %v", err)
	}

	rec := postJSON(t, res.Router, "/api/auth/v1/session", "application/json",
		map[string]string{"email": "Password@Example.com", "password": sessionUserPassword}, nil)
	cookie := findCookie(rec, sessionCookie)
	if rec.Code != http.StatusNoContent || cookie == nil || !strings.HasPrefix(cookie.Value, "bdbsn_") ||
		!cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 30*24*60*60 {
		t.Fatalf("sign-in = %d with cookie %+v; want 204 and an httpOnly SameSite=Strict 30-day bdbsn_ cookie",
			rec.Code, cookie)
	}
	if status, me := getWithCookie(t, res.Router, "/api/auth/v1/me", cookie.Value); status != http.StatusOK ||
		me["email"] != "password@example.com" {
		t.Errorf("GET /me with the cookie = %d %v, want 200 as the signed-in user", status, me)
	}
	if status, _ := testutil.DoRequest(t, res.Router, http.MethodGet, "/api/auth/v1/me", cookie.Value, nil); status !=
		http.StatusOK {
		t.Errorf("GET /me with the session as bearer token = %d, want 200", status)
	}
	if tokens, err := res.TokenSvc.List(ctx, userID); err != nil || len(tokens) != 0 {
		t.Errorf("personal tokens after the sign-in = %+v, %v; want none, the session being no personal token",
			tokens, err)
	}
	if rec := postJSON(t, res.Router, "/api/auth/v1/session", "text/plain",
		map[string]string{"email": "password@example.com", "password": sessionUserPassword}, nil); rec.Code !=
		http.StatusUnsupportedMediaType {
		t.Errorf("sign-in with a text/plain body = %d, want 415", rec.Code)
	}

	var refusals []string
	for name, creds := range map[string][2]string{
		"a wrong password":       {"password@example.com", "wrong-password"},
		"an unknown email":       {"nobody@example.com", sessionUserPassword},
		"an account without one": {"nopassword@example.com", ""},
	} {
		rec := postJSON(t, res.Router, "/api/auth/v1/session", "application/json",
			map[string]string{"email": creds[0], "password": creds[1]}, nil)
		if rec.Code != http.StatusUnauthorized || findCookie(rec, sessionCookie) != nil {
			t.Errorf("sign-in with %s = %d, want 401 and no cookie", name, rec.Code)
		}
		refusals = append(refusals, rec.Body.String())
	}
	if refusals[0] != refusals[1] || refusals[1] != refusals[2] {
		t.Errorf("the refusals differ: %q; want one answer, telling no account apart", refusals)
	}

	if rec := postJSON(t, res.Router, "/api/auth/v1/logout", "application/json", nil, cookie); rec.Code !=
		http.StatusNoContent {
		t.Errorf("logout = %d, want 204", rec.Code)
	}
	if status, _ := testutil.DoRequest(t, res.Router, http.MethodGet, "/api/auth/v1/me", cookie.Value, nil); status !=
		http.StatusUnauthorized {
		t.Errorf("GET /me with the ended session as bearer token = %d, want 401", status)
	}
	if rec := postJSON(t, res.Router, "/api/auth/v1/logout", "application/json", nil, nil); rec.Code !=
		http.StatusNoContent {
		t.Errorf("logout without a cookie = %d, want 204", rec.Code)
	}
}

// TestBrowserSession_PasswordChangeEndsSessions checks that a password change asked to keep the sessions keeps them,
// and that one that doesn't say ends every session of the user, the one making the change included, while their
// personal access token and another user's session keep working.
func TestBrowserSession_PasswordChangeEndsSessions(t *testing.T) {
	truncateAll(t)
	res := setupPersonalTokensOnly(t)
	userID := createSessionUser(t, res, "changer@example.com")
	createSessionUser(t, res, "bystander@example.com")
	personal, _, err := res.TokenSvc.Create(context.Background(), userID, "script", nil)
	if err != nil {
		t.Fatalf("creating a personal access token: %v", err)
	}
	mine, other := signInSession(t, res, "changer@example.com"), signInSession(t, res, "changer@example.com")
	bystander := signInSession(t, res, "bystander@example.com")
	signedIn := func(cookie *http.Cookie) bool {
		status, _ := getWithCookie(t, res.Router, "/api/auth/v1/me", cookie.Value)
		return status == http.StatusOK
	}

	if rec := postJSON(t, res.Router, "/api/auth/v1/password", "application/json", map[string]any{
		"old_password": sessionUserPassword, "new_password": "Second-Passw0rd!", "end_sessions": false,
	}, mine); rec.Code != http.StatusOK || !signedIn(mine) || !signedIn(other) {
		t.Fatalf("change keeping the sessions = %d %s, sessions working: %t %t; want 200 and both working",
			rec.Code, rec.Body, signedIn(mine), signedIn(other))
	}

	if rec := postJSON(t, res.Router, "/api/auth/v1/password", "application/json", map[string]any{
		"old_password": "Second-Passw0rd!", "new_password": "Third-Passw0rd!",
	}, mine); rec.Code != http.StatusOK {
		t.Fatalf("change = %d %s, want 200", rec.Code, rec.Body)
	}
	if signedIn(mine) || signedIn(other) {
		t.Errorf("sessions working after the change: %t %t; want both ended", signedIn(mine), signedIn(other))
	}
	if status, _ := testutil.DoRequest(t, res.Router, http.MethodGet, "/api/auth/v1/me", personal, nil); status !=
		http.StatusOK {
		t.Errorf("GET /me with the personal access token after the change = %d, want 200", status)
	}
	if !signedIn(bystander) {
		t.Error("another user's session ended with the change, want it working")
	}
}

// signInSession signs email in with sessionUserPassword through res's password sign-in and returns the session cookie.
func signInSession(t *testing.T, res *authmodule.SetupResult, email string) *http.Cookie {
	t.Helper()
	rec := postJSON(t, res.Router, "/api/auth/v1/session", "application/json",
		map[string]string{"email": email, "password": sessionUserPassword}, nil)
	cookie := findCookie(rec, sessionCookie)
	if rec.Code != http.StatusNoContent || cookie == nil {
		t.Fatalf("signing in as %s = %d with cookie %v, want 204 and a session cookie", email, rec.Code, cookie)
	}
	return cookie
}

// TestBrowserSession_OnlyWithTokensAlone checks that the browser session routes are mounted only when personal
// access tokens are the app's only credentials: the OAuth setup, even with personal access tokens on, has no password
// sign-in route of its own.
func TestBrowserSession_OnlyWithTokensAlone(t *testing.T) {
	truncateAll(t)
	res := setupWithOAuth(t, authmodule.SetupOptions{PersonalAccessTokens: true})
	if rec := postJSON(t, res.Router, "/api/auth/v1/session", "application/json",
		map[string]string{"email": "a@example.com", "password": "x"}, nil); rec.Code != http.StatusNotFound &&
		rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("sign-in on the OAuth setup = %d, want the route absent", rec.Code)
	}
}

// setupPersonalTokensOnly sets up the auth app without OAuth, with personal access tokens as its only credentials,
// against the shared test database, and releases it when the test ends.
func setupPersonalTokensOnly(t *testing.T) *authmodule.SetupResult {
	t.Helper()
	cfg := config.Config{InternalAPISecret: testutil.InternalAPISecret, PublicURL: "http://localhost:8080"}
	testDB.Configure(&cfg)
	res, err := authmodule.SetupWithoutOAuth(context.Background(), cfg, testLogger,
		authmodule.SetupOptions{PersonalAccessTokens: true})
	if err != nil {
		t.Fatalf("setting up the auth app with personal access tokens alone: %v", err)
	}
	t.Cleanup(res.Cleanup)
	return res
}

// sessionUserPassword is the password of the users createSessionUser creates.
const sessionUserPassword = "Str0ng-Passw0rd!"

// createSessionUser creates an active user with email and sessionUserPassword through res, and returns its ID.
func createSessionUser(t *testing.T, res *authmodule.SetupResult, email string) uuid.UUID {
	t.Helper()
	user, err := res.UserSvc.CreateUser(context.Background(), email, sessionUserPassword, "Session", "User")
	if err != nil {
		t.Fatalf("creating user %s: %v", email, err)
	}
	return user.ID
}

// postJSON posts body, encoded as JSON (none when nil) and declared as contentType, to path on handler, with cookie
// when not nil, and returns the recorded response.
func postJSON(
	t *testing.T, handler http.Handler, path, contentType string, body any, cookie *http.Cookie,
) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Errorf("encoding the body of POST %s: %v", path, err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", contentType)
	if cookie != nil {
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// getWithCookie sends GET path to handler with the session cookie set to value and no Authorization header, and
// returns the status and the JSON body.
func getWithCookie(t *testing.T, handler http.Handler, path, value string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var body map[string]any
	if rec.Body.Len() > 0 {
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Errorf("decoding GET %s: %v", path, err)
		}
	}
	return rec.Code, body
}

// findCookie returns the cookie named name that rec sets, or nil.
func findCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}
