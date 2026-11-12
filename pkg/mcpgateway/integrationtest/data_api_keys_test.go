//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	authdomain "github.com/alexandremgo/baguettedb/pkg/auth/domain"
	bdbsdomain "github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbsvc "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/instance"
)

// keysPath returns the path of the bdb bdbID's Data API key routes, followed by rest.
func keysPath(bdbID, rest string) string {
	return "/api/bdbs/v1/bdbs/" + bdbID + "/data-api-keys" + rest
}

// keyInstance is an instance whose routes a test calls as its users, each with a personal token.
type keyInstance struct {
	t    *testing.T
	inst *instance.Instance
}

// newKeyInstance composes every app with OAuth and personal tokens on, after clearing the suite's store.
func newKeyInstance(t *testing.T) *keyInstance {
	t.Helper()
	return newKeyInstanceWith(t, instance.OAuth(true))
}

// newKeyInstanceWith composes every app authenticating requests with auth, its configuration changed by tune, after
// clearing the suite's store.
func newKeyInstanceWith(t *testing.T, auth instance.Authentication, tune ...func(*config.Config)) *keyInstance {
	t.Helper()
	truncateAll(t)
	return &keyInstance{t: t, inst: newJWTInstance(t, auth, tune...)}
}

// user creates a user and returns their ID with a personal token of theirs.
func (k *keyInstance) user(name string) (uuid.UUID, string) {
	k.t.Helper()
	ctx := context.Background()
	user, err := k.inst.Auth.UserSvc.CreateUser(ctx, name+"-"+uuid.NewString()+"@example.com", "Str0ng-Passw0rd!",
		name, "Test")
	if err != nil {
		k.t.Fatalf("creating user %s: %v", name, err)
	}
	pat, _, err := k.inst.Auth.TokenSvc.Create(ctx, user.ID, "test", nil)
	if err != nil {
		k.t.Fatalf("creating a personal token for %s: %v", name, err)
	}
	return user.ID, pat
}

// do sends a request to the instance as the holder of token and returns the status and the decoded JSON body.
func (k *keyInstance) do(method, path, token string, body any) (int, map[string]any) {
	k.t.Helper()
	return testutil.DoRequest(k.t, k.inst.Router, method, path, token, body)
}

// bdbWithFormulas creates a bdb as the holder of token, with a notes table and one view formula per name, and returns
// the bdb's ID with the formulas' IDs in order.
func (k *keyInstance) bdbWithFormulas(token, name string, formulas ...string) (string, []string) {
	k.t.Helper()
	status, resp := k.do(http.MethodPost, "/api/bdbs/v1/bdbs", token, map[string]any{"name": name})
	if status != http.StatusCreated {
		k.t.Fatalf("creating bdb %s = %d %v, want 201", name, status, resp)
	}
	bdbID, _ := resp["id"].(string)

	status, resp = k.do(http.MethodPost, "/api/databases/v1/bdbs/"+bdbID+"/database/schema", token,
		map[string]any{"sql": "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)"})
	if status != http.StatusOK {
		k.t.Fatalf("creating bdb %s's table = %d %v, want 200", name, status, resp)
	}
	ids := make([]string, len(formulas))
	for i, formula := range formulas {
		status, resp = k.do(http.MethodPost, "/api/databases/v1/bdbs/"+bdbID+"/formulas", token, map[string]any{
			"name": formula, "description": "Lists the notes", "sql": "SELECT body FROM notes", "params": []any{},
		})
		if status != http.StatusCreated {
			k.t.Fatalf("creating formula %s = %d %v, want 201", formula, status, resp)
		}
		ids[i], _ = resp["id"].(string)
	}
	return bdbID, ids
}

// createKey creates a Data API key of the bdb bdbID as the holder of token, allowed to run formulaIDs, and returns its
// ID and plain value.
func (k *keyInstance) createKey(token, bdbID, name string, formulaIDs ...string) (string, string) {
	k.t.Helper()
	if formulaIDs == nil {
		formulaIDs = []string{}
	}
	status, resp := k.do(http.MethodPost, keysPath(bdbID, ""), token,
		map[string]any{"name": name, "formula_ids": formulaIDs})
	if status != http.StatusCreated {
		k.t.Fatalf("creating key %s = %d %v, want 201", name, status, resp)
	}
	id, _ := resp["id"].(string)
	value, _ := resp["token"].(string)
	return id, value
}

// keyFormulas returns the formula IDs of each of the bdb bdbID's keys listed to the holder of token, keyed by key ID.
func (k *keyInstance) keyFormulas(token, bdbID string) map[string][]string {
	k.t.Helper()
	status, resp := k.do(http.MethodGet, keysPath(bdbID, ""), token, nil)
	if status != http.StatusOK {
		k.t.Fatalf("listing keys = %d %v, want 200", status, resp)
	}
	listed := map[string][]string{}
	keys, _ := resp["keys"].([]any)
	for _, raw := range keys {
		key, _ := raw.(map[string]any)
		if _, hasValue := key["token"]; hasValue {
			k.t.Errorf("listed key %v carries its value", key)
		}
		id, _ := key["id"].(string)
		formulas, _ := key["formula_ids"].([]any)
		listed[id] = []string{}
		for _, f := range formulas {
			s, _ := f.(string)
			listed[id] = append(listed[id], s)
		}
	}
	return listed
}

// keyValid reports whether the Data API key value authenticates.
func (k *keyInstance) keyValid(value string) bool {
	k.t.Helper()
	_, err := k.inst.Auth.TokenSvc.CheckDataAPIKey(context.Background(), value)
	if err != nil && !errors.Is(err, authdomain.ErrInvalidAccessToken) {
		k.t.Fatalf("checking a data API key: %v", err)
	}
	return err == nil
}

// storedFormulaLinks returns how many formula links the bdb bdbID's keys have in the store.
func (k *keyInstance) storedFormulaLinks(bdbID string) int {
	k.t.Helper()
	var n int
	if err := testDB.QueryRow(context.Background(),
		`SELECT count(*) FROM data_api_key_formulas WHERE bdb_id = $1`, bdbID).Scan(&n); err != nil {
		k.t.Fatalf("counting formula links: %v", err)
	}
	return n
}

// TestDataAPIKeys_ManagedByTheOwner checks a bdb owner's management of the bdb's Data API keys over REST: a key
// created with a formula carries its value once and authenticates for that bdb; the list shows each key's formulas,
// never a value; a formula is added (twice without effect) and removed with the key's value unchanged; a key can start
// with no formula; an empty name is a 400, an unknown key a 404, a malformed key ID a 400; and a revoked key leaves the
// list, stops authenticating, and can't be revoked again.
func TestDataAPIKeys_ManagedByTheOwner(t *testing.T) {
	k := newKeyInstance(t)
	_, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Finance", "monthly", "yearly")

	keyID, value := k.createKey(owner, bdbID, "finance app", formulas[0])
	if !strings.HasPrefix(value, authdomain.AccessTokenDataAPIKey.Prefix()) {
		t.Fatalf("created key value = %q, want a %s value", value, authdomain.AccessTokenDataAPIKey.Prefix())
	}
	identity, err := k.inst.Auth.TokenSvc.CheckDataAPIKey(context.Background(), value)
	if err != nil || identity.BdbID.String() != bdbID || identity.KeyID.String() != keyID {
		t.Fatalf("CheckDataAPIKey = %+v, %v; want key %s of bdb %s", identity, err, keyID, bdbID)
	}
	if got := k.keyFormulas(owner, bdbID); !slices.Equal(got[keyID], formulas[:1]) {
		t.Fatalf("listed formulas = %v, want the key's one formula", got)
	}

	for range 2 {
		if status, resp := k.do(http.MethodPut, keysPath(bdbID, "/"+keyID+"/formulas/"+formulas[1]), owner,
			nil); status != http.StatusNoContent {
			t.Fatalf("adding a formula = %d %v, want 204", status, resp)
		}
	}
	if got := k.keyFormulas(owner, bdbID); !slices.Equal(got[keyID], formulas) {
		t.Errorf("listed formulas after adding one twice = %v, want both formulas once", got[keyID])
	}
	if status, resp := k.do(http.MethodDelete, keysPath(bdbID, "/"+keyID+"/formulas/"+formulas[0]), owner,
		nil); status != http.StatusNoContent {
		t.Fatalf("removing a formula = %d %v, want 204", status, resp)
	}
	if got := k.keyFormulas(owner, bdbID); !slices.Equal(got[keyID], formulas[1:]) {
		t.Errorf("listed formulas after removing one = %v, want the other alone", got[keyID])
	}
	if !k.keyValid(value) {
		t.Error("the key stopped authenticating after its formulas changed, want its value kept")
	}

	emptyID, _ := k.createKey(owner, bdbID, "not yet")
	if got := k.keyFormulas(owner, bdbID); got[emptyID] == nil || len(got[emptyID]) != 0 {
		t.Errorf("listed formulas of a key created with none = %v, want an empty list", got[emptyID])
	}

	for _, tt := range []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"an empty name", http.MethodPost, keysPath(bdbID, ""), map[string]any{"name": " "}, http.StatusBadRequest},
		{"an unknown key", http.MethodPut, keysPath(bdbID, "/"+uuid.NewString()+"/formulas/"+formulas[0]), nil,
			http.StatusNotFound},
		{"a malformed key ID", http.MethodDelete, keysPath(bdbID, "/nope"), nil, http.StatusBadRequest},
	} {
		if status, resp := k.do(tt.method, tt.path, owner, tt.body); status != tt.want {
			t.Errorf("%s %s with %s = %d %v, want %d", tt.method, tt.path, tt.name, status, resp, tt.want)
		}
	}

	if status, resp := k.do(http.MethodDelete, keysPath(bdbID, "/"+keyID), owner, nil); status !=
		http.StatusNoContent {
		t.Fatalf("revoking the key = %d %v, want 204", status, resp)
	}
	if got := k.keyFormulas(owner, bdbID); got[keyID] != nil || got[emptyID] == nil {
		t.Errorf("listed keys after revoking one = %v, want the other alone", got)
	}
	if k.keyValid(value) {
		t.Error("a revoked key still authenticates")
	}
	if status, _ := k.do(http.MethodDelete, keysPath(bdbID, "/"+keyID), owner, nil); status != http.StatusNotFound {
		t.Errorf("revoking the key again = %d, want 404", status)
	}
}

// TestDataAPIKeys_OwnerOnly checks that only a bdb's owner manages its Data API keys: an editor, a viewer and a user
// who isn't a member are refused every route with a 403, and a bdb that doesn't exist is a 404.
func TestDataAPIKeys_OwnerOnly(t *testing.T) {
	k := newKeyInstance(t)
	ownerID, owner := k.user("owner")
	editorID, editor := k.user("editor")
	viewerID, viewer := k.user("viewer")
	_, outsider := k.user("outsider")
	bdbID, formulas := k.bdbWithFormulas(owner, "Shared", "summary")
	keyID, _ := k.createKey(owner, bdbID, "owner app", formulas[0])

	ctx := context.Background()
	roles := []authz.Role{authz.RoleUser}
	members := map[uuid.UUID]authz.BdbRole{editorID: authz.BdbRoleEditor, viewerID: authz.BdbRoleViewer}
	for member, role := range members {
		if _, err := k.inst.Bdbs.BdbSvc.AddMember(ctx, ownerID, uuid.MustParse(bdbID), member, role,
			roles); err != nil {
			t.Fatalf("adding a %s: %v", role, err)
		}
	}

	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, keysPath(bdbID, ""), nil},
		{http.MethodPost, keysPath(bdbID, ""), map[string]any{"name": "mine", "formula_ids": []string{}}},
		{http.MethodPut, keysPath(bdbID, "/"+keyID+"/formulas/"+formulas[0]), nil},
		{http.MethodDelete, keysPath(bdbID, "/"+keyID+"/formulas/"+formulas[0]), nil},
		{http.MethodDelete, keysPath(bdbID, "/"+keyID), nil},
	}
	for name, token := range map[string]string{"an editor": editor, "a viewer": viewer, "a non-member": outsider} {
		for _, route := range routes {
			if status, resp := k.do(route.method, route.path, token, route.body); status != http.StatusForbidden {
				t.Errorf("%s %s as %s = %d %v, want 403", route.method, route.path, name, status, resp)
			}
		}
	}
	if status, _ := k.do(http.MethodGet, keysPath(uuid.NewString(), ""), owner, nil); status != http.StatusNotFound {
		t.Errorf("listing the keys of a bdb that doesn't exist = %d, want 404", status)
	}
}

// TestDataAPIKeys_RefuseFormulasOutsideTheBdb checks that a Data API key can only be given its own bdb's formulas:
// creating one with a formula of another bdb of the same owner, or with an ID naming no formula, is a 422, as is adding
// either to an existing key; nothing is stored for them.
func TestDataAPIKeys_RefuseFormulasOutsideTheBdb(t *testing.T) {
	k := newKeyInstance(t)
	_, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Finance", "monthly")
	_, otherFormulas := k.bdbWithFormulas(owner, "Sport", "runs")
	keyID, _ := k.createKey(owner, bdbID, "finance app", formulas[0])

	outside := map[string]string{"another bdb's formula": otherFormulas[0], "an unknown formula ID": "nope"}
	for name, formulaID := range outside {
		if status, resp := k.do(http.MethodPost, keysPath(bdbID, ""), owner,
			map[string]any{"name": "app", "formula_ids": []string{formulas[0], formulaID}}); status !=
			http.StatusUnprocessableEntity {
			t.Errorf("creating a key with %s = %d %v, want 422", name, status, resp)
		}
		if status, resp := k.do(http.MethodPut, keysPath(bdbID, "/"+keyID+"/formulas/"+formulaID), owner,
			nil); status != http.StatusUnprocessableEntity {
			t.Errorf("adding %s to a key = %d %v, want 422", name, status, resp)
		}
	}
	if got := k.keyFormulas(owner, bdbID); len(got) != 1 || !slices.Equal(got[keyID], formulas) {
		t.Errorf("listed keys = %v, want the first key alone, with its one formula", got)
	}
}

// TestDataAPIKeys_RevokedWithTheirBdbOrMember checks that Data API keys go with what they depend on. Soft-deleting a
// bdb revokes its keys and forgets their formulas, a restored bdb starts with none, and no key can be created on a
// deleted one. Removing a member revokes the keys they created, leaving the owner's. The purge revokes a key a
// soft-deleted bdb still had before removing the bdb.
func TestDataAPIKeys_RevokedWithTheirBdbOrMember(t *testing.T) {
	k := newKeyInstance(t)
	ownerID, owner := k.user("owner")
	editorID, _ := k.user("editor")
	ctx := context.Background()

	deletedID, deletedFormulas := k.bdbWithFormulas(owner, "Deleted", "summary")
	_, deletedKey := k.createKey(owner, deletedID, "app", deletedFormulas[0])
	if status, resp := k.do(http.MethodDelete, "/api/bdbs/v1/bdbs/"+deletedID, owner, nil); status !=
		http.StatusNoContent {
		t.Fatalf("soft-deleting the bdb = %d %v, want 204", status, resp)
	}
	if k.keyValid(deletedKey) || k.storedFormulaLinks(deletedID) != 0 {
		t.Errorf("after soft-deleting its bdb, key valid = %v and %d formula links left; want revoked, none left",
			k.keyValid(deletedKey), k.storedFormulaLinks(deletedID))
	}
	if status, _ := k.do(http.MethodPost, keysPath(deletedID, ""), owner,
		map[string]any{"name": "late", "formula_ids": []string{}}); status != http.StatusConflict {
		t.Errorf("creating a key on a soft-deleted bdb = %d, want 409", status)
	}
	if status, resp := k.do(http.MethodPost, "/api/bdbs/v1/bdbs/"+deletedID+"/restore", owner, nil); status !=
		http.StatusOK {
		t.Fatalf("restoring the bdb = %d %v, want 200", status, resp)
	}
	if got := k.keyFormulas(owner, deletedID); len(got) != 0 {
		t.Errorf("keys of the restored bdb = %v, want none", got)
	}

	sharedID, sharedFormulas := k.bdbWithFormulas(owner, "Shared", "summary")
	if _, err := k.inst.Bdbs.BdbSvc.AddMember(ctx, ownerID, uuid.MustParse(sharedID), editorID,
		authz.BdbRoleEditor, []authz.Role{authz.RoleUser}); err != nil {
		t.Fatalf("adding the editor: %v", err)
	}
	_, ownerKey := k.createKey(owner, sharedID, "owner app", sharedFormulas[0])
	// Only the owner creates keys over REST; this one stands for a key an editor could create once they may.
	editorKey, _, err := k.inst.Auth.TokenSvc.CreateDataAPIKey(ctx, editorID, uuid.MustParse(sharedID), "editor app")
	if err != nil {
		t.Fatalf("creating the editor's key: %v", err)
	}
	if status, resp := k.do(http.MethodDelete, "/api/bdbs/v1/bdbs/"+sharedID+"/members/"+editorID.String(), owner,
		nil); status != http.StatusOK && status != http.StatusNoContent {
		t.Fatalf("removing the editor = %d %v, want success", status, resp)
	}
	if k.keyValid(editorKey) || !k.keyValid(ownerKey) {
		t.Errorf("after removing the editor, their key valid = %v and the owner's = %v; want false and true",
			k.keyValid(editorKey), k.keyValid(ownerKey))
	}

	purgedID, _ := k.bdbWithFormulas(owner, "Purged")
	if status, resp := k.do(http.MethodDelete, "/api/bdbs/v1/bdbs/"+purgedID, owner, nil); status !=
		http.StatusNoContent {
		t.Fatalf("soft-deleting the bdb to purge = %d %v, want 204", status, resp)
	}
	// Stands for a key a failed revocation left on the soft-deleted bdb.
	leftKey, _, err := k.inst.Auth.TokenSvc.CreateDataAPIKey(ctx, ownerID, uuid.MustParse(purgedID), "left")
	if err != nil {
		t.Fatalf("creating the key left behind: %v", err)
	}
	stats, err := k.inst.Bdbs.BdbSvc.PurgeExpired(ctx, time.Now().Add(bdbsdomain.GracePeriod+time.Minute))
	if err != nil || stats.Failed != 0 || stats.Purged == 0 {
		t.Fatalf("PurgeExpired = %+v, %v; want the bdb purged", stats, err)
	}
	if k.keyValid(leftKey) {
		t.Error("the purge left the soft-deleted bdb's key valid")
	}
}

// actionFormula creates an action formula adding a note in the bdb bdbID as the holder of token, and returns its ID.
func (k *keyInstance) actionFormula(token, bdbID, name string) string {
	k.t.Helper()
	status, resp := k.do(http.MethodPost, "/api/databases/v1/bdbs/"+bdbID+"/formulas", token, map[string]any{
		"name": name, "description": "Adds a note", "sql": "INSERT INTO notes (body) VALUES ('added')",
		"params": []any{},
	})
	if status != http.StatusCreated {
		k.t.Fatalf("creating action formula %s = %d %v, want 201", name, status, resp)
	}
	id, _ := resp["id"].(string)
	return id
}

// storeLink records directly that the key keyID may run formulaID on the bdb bdbID, for a state REST can't reach.
func (k *keyInstance) storeLink(bdbID, keyID, formulaID string) {
	k.t.Helper()
	if err := testDB.Exec(context.Background(),
		`INSERT INTO data_api_key_formulas (key_id, bdb_id, formula_id, created_at) VALUES ($1, $2, $3, $4)`,
		keyID, bdbID, formulaID, time.Now().UTC()); err != nil {
		k.t.Fatalf("storing a formula link: %v", err)
	}
}

// run runs formulaID under the key keyID of the bdb bdbID, acting as userID.
func (k *keyInstance) run(userID uuid.UUID, keyID, bdbID, formulaID string) (*dbsvc.FormulaRunResult, error) {
	k.t.Helper()
	return k.inst.Databases.FormulaSvc.RunFormulaAsDataAPIKey(context.Background(), userID, uuid.MustParse(keyID),
		uuid.MustParse(bdbID), formulaID, dbsvc.FormulaRunInput{})
}

// TestDataAPIKeys_RunFormula checks a run under a Data API key: a view and an action on the key run, the action's
// change showing in the view, while a formula of the bdb that isn't on the key, a formula named rather than given by
// ID, and a formula on the key asked for on another bdb are all refused.
func TestDataAPIKeys_RunFormula(t *testing.T) {
	k := newKeyInstance(t)
	ownerID, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Notes", "all-notes", "other-notes")
	otherBdbID, _ := k.bdbWithFormulas(owner, "Elsewhere")
	action := k.actionFormula(owner, bdbID, "add-note")
	keyID, _ := k.createKey(owner, bdbID, "app", formulas[0], action)

	if _, err := k.run(ownerID, keyID, bdbID, action); err != nil {
		t.Fatalf("running the action on the key = %v", err)
	}
	result, err := k.run(ownerID, keyID, bdbID, formulas[0])
	if err != nil {
		t.Fatalf("running the view on the key = %v", err)
	}
	if len(result.Result.Rows) != 1 {
		t.Errorf("view rows = %v, want the note the action added", result.Result.Rows)
	}

	for name, tt := range map[string]struct{ bdb, formula string }{
		"a formula not on the key":             {bdbID, formulas[1]},
		"a formula by name":                    {bdbID, "all-notes"},
		"a formula of the key, on another bdb": {otherBdbID, formulas[0]},
	} {
		if _, err := k.run(ownerID, keyID, tt.bdb, tt.formula); !errors.Is(err, dbdomain.ErrAccessDenied) {
			t.Errorf("running %s = %v, want ErrAccessDenied", name, err)
		}
	}
}

// TestDataAPIKeys_RunRefusedOnASoftDeletedBdb checks that no key runs anything on a soft-deleted bdb, even a key a
// failed revocation left there with its formula.
func TestDataAPIKeys_RunRefusedOnASoftDeletedBdb(t *testing.T) {
	k := newKeyInstance(t)
	ownerID, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Notes", "all-notes")
	keyID, _ := k.createKey(owner, bdbID, "app", formulas[0])

	if status, resp := k.do(http.MethodDelete, "/api/bdbs/v1/bdbs/"+bdbID, owner, nil); status !=
		http.StatusNoContent {
		t.Fatalf("soft-deleting the bdb = %d %v, want 204", status, resp)
	}
	k.storeLink(bdbID, keyID, formulas[0])
	if _, err := k.run(ownerID, keyID, bdbID, formulas[0]); !errors.Is(err, dbdomain.ErrAccessDenied) {
		t.Errorf("running on a soft-deleted bdb = %v, want ErrAccessDenied", err)
	}
}

// TestDataAPIKeys_RunStaysWithinTheCreatorsRights checks that a key never does more than the user it acts as: a
// viewer's key runs a view but not an action, even with both on it, and runs nothing once the viewer has left the bdb.
// Only the owner creates keys over REST today, so the key is recorded for the viewer directly.
func TestDataAPIKeys_RunStaysWithinTheCreatorsRights(t *testing.T) {
	k := newKeyInstance(t)
	ownerID, owner := k.user("owner")
	viewerID, _ := k.user("viewer")
	bdbID, formulas := k.bdbWithFormulas(owner, "Shared", "all-notes")
	action := k.actionFormula(owner, bdbID, "add-note")

	ctx := context.Background()
	if _, err := k.inst.Bdbs.BdbSvc.AddMember(ctx, ownerID, uuid.MustParse(bdbID), viewerID, authz.BdbRoleViewer,
		[]authz.Role{authz.RoleUser}); err != nil {
		t.Fatalf("adding the viewer: %v", err)
	}
	_, key, err := k.inst.Auth.TokenSvc.CreateDataAPIKey(ctx, viewerID, uuid.MustParse(bdbID), "viewer app")
	if err != nil {
		t.Fatalf("creating the viewer's key: %v", err)
	}
	keyID := key.ID.String()
	k.storeLink(bdbID, keyID, formulas[0])
	k.storeLink(bdbID, keyID, action)

	if _, err := k.run(viewerID, keyID, bdbID, formulas[0]); err != nil {
		t.Errorf("running a view under a viewer's key = %v", err)
	}
	if _, err := k.run(viewerID, keyID, bdbID, action); !errors.Is(err, dbdomain.ErrAccessDenied) {
		t.Errorf("running an action under a viewer's key = %v, want ErrAccessDenied", err)
	}

	if _, err := k.inst.Bdbs.BdbSvc.RemoveMember(ctx, ownerID, uuid.MustParse(bdbID), viewerID); err != nil {
		t.Fatalf("removing the viewer: %v", err)
	}
	if _, err := k.run(viewerID, keyID, bdbID, formulas[0]); !errors.Is(err, dbdomain.ErrAccessDenied) {
		t.Errorf("running under a removed member's key = %v, want ErrAccessDenied", err)
	}
}

// TestDataAPIKeys_DeletingAFormulaForgetsItsLinks checks that deleting a formula removes it from every key that had
// it, leaving their other formulas.
func TestDataAPIKeys_DeletingAFormulaForgetsItsLinks(t *testing.T) {
	k := newKeyInstance(t)
	_, owner := k.user("owner")
	bdbID, formulas := k.bdbWithFormulas(owner, "Notes", "all-notes", "other-notes")
	firstKey, _ := k.createKey(owner, bdbID, "first app", formulas...)
	secondKey, _ := k.createKey(owner, bdbID, "second app", formulas[0])

	if status, resp := k.do(http.MethodDelete, "/api/databases/v1/bdbs/"+bdbID+"/formulas/"+formulas[0], owner,
		nil); status != http.StatusNoContent {
		t.Fatalf("deleting the formula = %d %v, want 204", status, resp)
	}
	got := k.keyFormulas(owner, bdbID)
	if !slices.Equal(got[firstKey], formulas[1:]) || len(got[secondKey]) != 0 {
		t.Errorf("keys' formulas after deleting one = %v, want the other formula on the first key alone", got)
	}
}

// TestDataAPIKeys_RunForgetsAMissingFormula checks that a key calling a formula that no longer exists, whose link a
// failed clean-up left behind, gets a not-found error and the link is removed.
func TestDataAPIKeys_RunForgetsAMissingFormula(t *testing.T) {
	k := newKeyInstance(t)
	ownerID, owner := k.user("owner")
	bdbID, _ := k.bdbWithFormulas(owner, "Notes")
	keyID, _ := k.createKey(owner, bdbID, "app")
	missing := uuid.NewString()
	k.storeLink(bdbID, keyID, missing)

	if _, err := k.run(ownerID, keyID, bdbID, missing); !errors.Is(err, dbdomain.ErrFormulaNotFound) {
		t.Fatalf("running a formula that no longer exists = %v, want ErrFormulaNotFound", err)
	}
	if links := k.storedFormulaLinks(bdbID); links != 0 {
		t.Errorf("formula links left after running a missing formula = %d, want none", links)
	}
}
