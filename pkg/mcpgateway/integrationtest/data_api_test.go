//go:build integration

package integrationtest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
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
// parameters; an action answers how many rows it changed and the rowid it inserted; a view answers its rows as objects
// by column; and a paginated view answers a page at a time with the cursor of the next one.
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
// invalid_key); and a request carrying a token in a cookie but no key (401 missing_key), since the Data API reads no
// cookie.
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
		{"a cookie but no key", http.MethodGet, dataAPIPath(bdbID, "/formulas"), "", nil,
			map[string]string{"Cookie": "session=" + owner}, http.StatusUnauthorized, "missing_key"},
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
