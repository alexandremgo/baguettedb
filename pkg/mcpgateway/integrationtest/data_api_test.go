//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/instance"
)

// dataAPIPath returns the path of a Data API route of the bdb bdbID, followed by rest.
func dataAPIPath(bdbID, rest string) string {
	return "/api/data/v1/bdbs/" + bdbID + rest
}

// dataCall sends a request to the instance's Data API, with key as its bearer token unless key is empty, body as JSON
// unless it is nil, and the extra headers, then returns the status, the decoded JSON body, and the response headers.
func (k *keyInstance) dataCall(
	method, path, key string, body any, headers map[string]string,
) (int, map[string]any, http.Header) {
	k.t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		if encoded, err = json.Marshal(body); err != nil {
			k.t.Fatalf("marshaling the request body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	k.inst.Router.ServeHTTP(rec, req)

	var resp map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			k.t.Fatalf("decoding %s %s's body %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec.Code, resp, rec.Header()
}

// pagedFormula creates a view of the bdb bdbID's notes paginated with the reserved :cursor and :limit parameters, as
// the holder of token, and returns its ID.
func (k *keyInstance) pagedFormula(token, bdbID, name string) string {
	k.t.Helper()
	status, resp := k.do(http.MethodPost, "/api/databases/v1/bdbs/"+bdbID+"/formulas", token, map[string]any{
		"name": name, "description": "Notes a page at a time",
		"sql": "SELECT rowid AS rowid, body FROM notes WHERE rowid > :cursor ORDER BY rowid LIMIT :limit",
		"params": []map[string]any{
			{"name": "cursor", "type": "integer", "source": "pagination"},
			{"name": "limit", "type": "integer", "source": "pagination"},
		},
	})
	if status != http.StatusCreated {
		k.t.Fatalf("creating paged formula %s = %d %v, want 201", name, status, resp)
	}
	id, _ := resp["id"].(string)
	return id
}

// errorCode returns the code of a Data API error body.
func errorCode(resp map[string]any) string {
	code, _ := resp["code"].(string)
	return code
}

// TestDataAPI_ListAndRun checks the Data API with a key: listing gives the key's formulas alone, with their
// parameters and a view's columns; an action answers how many rows it changed and the rowid it inserted; a view
// answers its rows as objects by column; and a paginated view answers a page at a time with the cursor of the next
// one.
func TestDataAPI_ListAndRun(t *testing.T) {
	k := newKeyInstance(t)
	_, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Notes", "all-notes", "other-notes")
	action := k.actionFormula(owner, bdbID, "add-note")
	paged := k.pagedFormula(owner, bdbID, "note-pages")
	_, key := k.createKey(owner, bdbID, "app", formulas[0], action, paged)

	status, resp, _ := k.dataCall(http.MethodGet, dataAPIPath(bdbID, "/formulas"), key, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("listing the key's formulas = %d %v, want 200", status, resp)
	}
	listed, _ := resp["formulas"].([]any)
	var ids []string
	var pagedParams []any
	wantColumns := map[string]string{formulas[0]: "[body]", paged: "[rowid body]", action: "[]"}
	for _, raw := range listed {
		f, _ := raw.(map[string]any)
		id, _ := f["id"].(string)
		ids = append(ids, id)
		if id == paged {
			pagedParams, _ = f["params"].([]any)
		}
		if _, hasSQL := f["sql"]; hasSQL {
			t.Errorf("listed formula %v carries its SQL", f)
		}
		columns, _ := f["columns"].([]any)
		if got := fmt.Sprint(columns); got != wantColumns[id] {
			t.Errorf("listed formula %v's columns = %s, want %s", f["name"], got, wantColumns[id])
		}
	}
	slices.Sort(ids)
	want := []string{formulas[0], action, paged}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("listed formulas = %v, want the key's three", ids)
	}
	if len(pagedParams) != 2 {
		t.Fatalf("paged formula params = %v, want cursor and limit", pagedParams)
	}
	for _, raw := range pagedParams {
		p, _ := raw.(map[string]any)
		if p["source"] != "pagination" || p["required"] != false {
			t.Errorf("paged formula param = %v, want an optional pagination parameter", p)
		}
	}

	for range 2 {
		status, resp, _ = k.dataCall(http.MethodPost, dataAPIPath(bdbID, "/formulas/"+action+"/run"), key, nil, nil)
		if status != http.StatusOK || resp["affected_row_count"] != float64(1) || resp["last_insert_row_id"] == nil {
			t.Fatalf("running the action = %d %v, want one row changed and its rowid", status, resp)
		}
	}

	status, resp, _ = k.dataCall(http.MethodPost, dataAPIPath(bdbID, "/formulas/"+formulas[0]+"/run"), key,
		map[string]any{"params": map[string]any{}}, nil)
	rows, _ := resp["rows"].([]any)
	if status != http.StatusOK || len(rows) != 2 {
		t.Fatalf("running the view = %d %v, want the two notes", status, resp)
	}
	if row, _ := rows[0].(map[string]any); row["body"] != "added" {
		t.Errorf("view row = %v, want an object with the note's body", rows[0])
	}

	status, resp, _ = k.dataCall(http.MethodPost, dataAPIPath(bdbID, "/formulas/"+paged+"/run"), key,
		map[string]any{"params": map[string]any{"limit": 1}}, nil)
	rows, _ = resp["rows"].([]any)
	page, _ := resp["pagination"].(map[string]any)
	if status != http.StatusOK || len(rows) != 1 || page["has_more"] != true || page["next_cursor"] == nil {
		t.Fatalf("first page = %d %v, want one row and a next cursor", status, resp)
	}
	status, resp, _ = k.dataCall(http.MethodPost, dataAPIPath(bdbID, "/formulas/"+paged+"/run"), key,
		map[string]any{"params": map[string]any{"limit": 1, "cursor": page["next_cursor"]}}, nil)
	rows, _ = resp["rows"].([]any)
	page, _ = resp["pagination"].(map[string]any)
	if status != http.StatusOK || len(rows) != 1 || page["has_more"] != false {
		t.Errorf("second page = %d %v, want the last row and no more", status, resp)
	}
}

// TestDataAPI_Refusals checks what the Data API refuses, each with its code: a formula not on the key, or deleted
// since it was put there (403 formula_not_on_key); an invalid parameter value (400 invalid_params); a body that isn't
// JSON (400 invalid_request); no key (401 missing_key); a revoked key and a personal token as bearer tokens (401
// invalid_key); and the owner's personal token in the web app's access-token cookie, which the REST API accepts, but no
// key (401 missing_key), since the Data API reads no cookie.
func TestDataAPI_Refusals(t *testing.T) {
	k := newKeyInstance(t)
	_, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Notes", "all-notes", "other-notes", "doomed")
	paged := k.pagedFormula(owner, bdbID, "note-pages")
	_, key := k.createKey(owner, bdbID, "app", formulas[0], formulas[2], paged)
	revokedID, revoked := k.createKey(owner, bdbID, "revoked", formulas[0])
	if status, resp := k.do(http.MethodDelete, keysPath(bdbID, "/"+revokedID), owner, nil); status !=
		http.StatusNoContent {
		t.Fatalf("revoking a key = %d %v, want 204", status, resp)
	}
	if status, resp := k.do(http.MethodDelete, "/api/databases/v1/bdbs/"+bdbID+"/formulas/"+formulas[2], owner,
		nil); status != http.StatusNoContent {
		t.Fatalf("deleting a formula = %d %v, want 204", status, resp)
	}
	accessCookie := authhttp.DefaultCookieName + "=" + owner
	if status := k.getWithCookie("/api/bdbs/v1/bdbs", accessCookie); status != http.StatusOK {
		t.Fatalf("listing bdbs with the access-token cookie = %d, want 200", status)
	}

	run := func(formulaID string) string { return dataAPIPath(bdbID, "/formulas/"+formulaID+"/run") }
	for _, tt := range []struct {
		name, method, path, key string
		body                    any
		headers                 map[string]string
		wantStatus              int
		wantCode                string
	}{
		{"a formula not on the key", http.MethodPost, run(formulas[1]), key, nil, nil,
			http.StatusForbidden, "formula_not_on_key"},
		{"a formula deleted since", http.MethodPost, run(formulas[2]), key, nil, nil,
			http.StatusForbidden, "formula_not_on_key"},
		{"an invalid parameter value", http.MethodPost, run(paged), key,
			map[string]any{"params": map[string]any{"limit": "lots"}}, nil, http.StatusBadRequest, "invalid_params"},
		{"a body that isn't JSON", http.MethodPost, run(formulas[0]), key, "not an object", nil,
			http.StatusBadRequest, "invalid_request"},
		{"no key", http.MethodGet, dataAPIPath(bdbID, "/formulas"), "", nil, nil,
			http.StatusUnauthorized, "missing_key"},
		{"a revoked key", http.MethodGet, dataAPIPath(bdbID, "/formulas"), revoked, nil, nil,
			http.StatusUnauthorized, "invalid_key"},
		{"a personal token", http.MethodGet, dataAPIPath(bdbID, "/formulas"), owner, nil, nil,
			http.StatusUnauthorized, "invalid_key"},
		{"the access-token cookie but no key", http.MethodGet, dataAPIPath(bdbID, "/formulas"), "", nil,
			map[string]string{"Cookie": accessCookie}, http.StatusUnauthorized, "missing_key"},
	} {
		status, resp, _ := k.dataCall(tt.method, tt.path, tt.key, tt.body, tt.headers)
		if status != tt.wantStatus || errorCode(resp) != tt.wantCode {
			t.Errorf("%s = %d %v, want %d %s", tt.name, status, resp, tt.wantStatus, tt.wantCode)
		}
	}

	status, resp, _ := k.dataCall(http.MethodGet, dataAPIPath(bdbID, "/formulas"), key, nil, nil)
	listed, _ := resp["formulas"].([]any)
	if status != http.StatusOK || len(listed) != 2 {
		t.Errorf("listing after a formula was deleted = %d %v, want the key's two remaining formulas", status, resp)
	}
}

// TestDataAPI_KeyBoundToItsBdb checks that a key is refused on the path of another bdb, even one of its own owner's,
// with a 403 key_not_for_bdb, before anything of that bdb is looked at.
func TestDataAPI_KeyBoundToItsBdb(t *testing.T) {
	k := newKeyInstance(t)
	_, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Notes", "all-notes")
	otherBdbID, _ := k.bdbWithFormulas(owner, "Elsewhere")
	_, key := k.createKey(owner, bdbID, "app", formulas[0])

	status, resp, _ := k.dataCall(http.MethodPost, dataAPIPath(otherBdbID, "/formulas/"+formulas[0]+"/run"), key,
		nil, nil)
	if status != http.StatusForbidden || errorCode(resp) != "key_not_for_bdb" {
		t.Errorf("a key on another bdb's path = %d %v, want 403 key_not_for_bdb", status, resp)
	}
}

// TestDataAPI_AnyOriginMayCall checks the Data API's cross-origin answers: a preflight from any origin is answered
// with a 204 allowing any origin and the Authorization header, with a max age for the browser to reuse it, before any
// key is asked for; an answer, a refusal included, allows any origin so the page can read it; and credentials are never
// allowed.
func TestDataAPI_AnyOriginMayCall(t *testing.T) {
	k := newKeyInstance(t)
	_, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Notes", "all-notes")
	_, key := k.createKey(owner, bdbID, "app", formulas[0])
	origin := map[string]string{"Origin": "http://localhost:5173"}

	status, _, headers := k.dataCall(http.MethodOptions, dataAPIPath(bdbID, "/formulas/"+formulas[0]+"/run"), "",
		nil, map[string]string{
			"Origin":                         "http://localhost:5173",
			"Access-Control-Request-Method":  "POST",
			"Access-Control-Request-Headers": "authorization,content-type",
		})
	allowedHeaders := strings.ToLower(headers.Get("Access-Control-Allow-Headers"))
	if status != http.StatusNoContent || headers.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(allowedHeaders, "authorization") || !strings.Contains(allowedHeaders, "content-type") ||
		headers.Get("Access-Control-Max-Age") == "" {
		t.Errorf("preflight = %d %v, want 204 allowing any origin, Authorization and Content-Type, with a max age",
			status, headers)
	}

	for name, call := range map[string]string{"an answer": key, "a refusal": ""} {
		_, _, headers = k.dataCall(http.MethodGet, dataAPIPath(bdbID, "/formulas"), call, nil, origin)
		if headers.Get("Access-Control-Allow-Origin") != "*" || headers.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%s's headers = %v, want any origin allowed and no credentials", name, headers)
		}
	}
}

// getWithCookie sends a GET of path to the instance carrying the Cookie header cookie and no bearer token, and returns
// the status.
func (k *keyInstance) getWithCookie(path, cookie string) int {
	k.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	k.inst.Router.ServeHTTP(rec, req)
	return rec.Code
}

// browserSession signs the user userID in as a browser does on an instance without OAuth, with their email and
// password, and returns the session cookie the instance sets.
func (k *keyInstance) browserSession(userID uuid.UUID) *http.Cookie {
	k.t.Helper()
	user, err := k.inst.Auth.UserSvc.GetProfile(context.Background(), userID)
	if err != nil {
		k.t.Fatalf("looking up user %s: %v", userID, err)
	}
	body, err := json.Marshal(map[string]string{"email": user.Email, "password": "Str0ng-Passw0rd!"})
	if err != nil {
		k.t.Fatalf("marshaling the sign-in body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/v1/session", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	k.inst.Router.ServeHTTP(rec, req)
	cookie, err := http.ParseSetCookie(rec.Header().Get("Set-Cookie"))
	if rec.Code != http.StatusNoContent || err != nil {
		k.t.Fatalf("signing in = %d %v (%v), want 204 with a session cookie", rec.Code, rec.Header(), err)
	}
	return cookie
}

// TestDataAPI_PersonalTokensOnly checks the Data API on an instance accepting personal tokens alone, as bdb serve
// does: a key lists its formula and runs it, a preflight is answered, and the owner's browser session, which the REST
// API accepts, is refused on the Data API for want of a key.
func TestDataAPI_PersonalTokensOnly(t *testing.T) {
	// An owner with a bdb, one formula, and a key allowed to run it.
	k := newKeyInstanceWith(t, instance.PersonalTokensOnly())
	ownerID, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Notes", "all-notes")
	_, key := k.createKey(owner, bdbID, "app", formulas[0])
	list, run := dataAPIPath(bdbID, "/formulas"), dataAPIPath(bdbID, "/formulas/"+formulas[0]+"/run")

	// The key is accepted in this mode: it lists its formula and runs it.
	status, resp, _ := k.dataCall(http.MethodGet, list, key, nil, nil)
	if listed, _ := resp["formulas"].([]any); status != http.StatusOK || len(listed) != 1 {
		t.Errorf("listing the key's formulas = %d %v, want its one formula", status, resp)
	}
	if status, resp, _ = k.dataCall(http.MethodPost, run, key, nil, nil); status != http.StatusOK {
		t.Errorf("running the key's formula = %d %v, want 200", status, resp)
	}

	// A browser's preflight is answered, from any origin.
	status, _, headers := k.dataCall(http.MethodOptions, run, "", nil, map[string]string{
		"Origin":                        "http://localhost:5173",
		"Access-Control-Request-Method": "POST",
	})
	if status != http.StatusNoContent || headers.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("preflight = %d %v, want 204 allowing any origin", status, headers)
	}

	// A browser session works on the REST API, but not on the Data API, which reads no cookie.
	session := k.browserSession(ownerID)
	sessionCookie := session.Name + "=" + session.Value
	if status := k.getWithCookie("/api/bdbs/v1/bdbs", sessionCookie); status != http.StatusOK {
		t.Fatalf("listing bdbs with the browser session = %d, want 200", status)
	}
	status, resp, _ = k.dataCall(http.MethodGet, list, "", nil, map[string]string{"Cookie": sessionCookie})
	if status != http.StatusUnauthorized || errorCode(resp) != "missing_key" {
		t.Errorf("the Data API with the browser session = %d %v, want 401 missing_key", status, resp)
	}
}

// runawaySQL is a query that only a time budget ends.
const runawaySQL = "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r) SELECT COUNT(*) AS n FROM r"

// TestDataAPI_KeyHasItsOwnSlots checks that runs through a key take in-flight slots of their own, as many as its
// owner's: with one slot each, the key runs while its owner holds theirs with a runaway query, and while the key holds
// its own with a runaway view, a second run through it is refused with 429 rate_limited and its owner still queries.
func TestDataAPI_KeyHasItsOwnSlots(t *testing.T) {
	k := newKeyInstanceWith(t, instance.OAuth(true), func(cfg *config.Config) {
		cfg.QuotaMaxConcurrentQueries = 1
		cfg.DatabasesMaxQueryDuration = 2 * time.Second
		cfg.DatabasesFormulaRunTimeout = 2 * time.Second
	})
	_, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Notes", "all-notes")
	status, resp := k.do(http.MethodPost, "/api/databases/v1/bdbs/"+bdbID+"/formulas", owner, map[string]any{
		"name": "runaway", "description": "Counts forever", "sql": runawaySQL, "params": []any{},
	})
	if status != http.StatusCreated {
		t.Fatalf("creating the runaway view = %d %v, want 201", status, resp)
	}
	runaway, _ := resp["id"].(string)
	_, key := k.createKey(owner, bdbID, "app", formulas[0], runaway)
	query := "/api/databases/v1/bdbs/" + bdbID + "/database/query"

	// hold runs do in the background, long enough for it to take its slot, and returns the wait for its end.
	hold := func(do func()) func() {
		var wg sync.WaitGroup
		wg.Go(do)
		time.Sleep(300 * time.Millisecond)
		return wg.Wait
	}

	wait := hold(func() { k.do(http.MethodPost, query, owner, map[string]string{"sql": runawaySQL}) })
	status, resp, _ = k.dataCall(http.MethodPost, dataAPIPath(bdbID, "/formulas/"+formulas[0]+"/run"), key, nil, nil)
	if status != http.StatusOK {
		t.Errorf("a run through the key while its owner holds their slot = %d %v, want 200", status, resp)
	}
	wait()

	wait = hold(func() { k.dataCall(http.MethodPost, dataAPIPath(bdbID, "/formulas/"+runaway+"/run"), key, nil, nil) })
	status, resp, _ = k.dataCall(http.MethodPost, dataAPIPath(bdbID, "/formulas/"+formulas[0]+"/run"), key, nil, nil)
	if status != http.StatusTooManyRequests || errorCode(resp) != "rate_limited" {
		t.Errorf("a second run through the key while it holds its slot = %d %v, want 429 rate_limited", status, resp)
	}
	status, resp = k.do(http.MethodPost, query, owner, map[string]string{"sql": "SELECT 1"})
	if status != http.StatusOK {
		t.Errorf("the owner's query while the key holds its slot = %d %v, want 200", status, resp)
	}
	wait()
}
