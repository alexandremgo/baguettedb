//go:build integration

package integrationtest

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// mcpSession opens an MCP session on the instance as the holder of token.
func (k *keyInstance) mcpSession(token string) *mcpclient.Client {
	k.t.Helper()
	ts := httptest.NewServer(k.inst.Router)
	k.t.Cleanup(ts.Close)
	c, err := mcpclient.NewStreamableHttpClient(ts.URL+"/mcp",
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		k.t.Fatalf("creating the MCP client: %v", err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		k.t.Fatalf("starting the MCP client: %v", err)
	}
	k.t.Cleanup(func() {
		if err := c.Close(); err != nil {
			k.t.Errorf("closing the MCP client: %v", err)
		}
	})
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "test-client", Version: "1.0.0"},
	}}); err != nil {
		k.t.Fatalf("initializing the MCP session: %v", err)
	}
	return c
}

// sortedKeyNames returns the names of the Data API keys a formula of an MCP answer carries, sorted.
func sortedKeyNames(formula map[string]any) []string {
	keys, _ := formula["data_api_keys"].([]any)
	names := []string{}
	for _, raw := range keys {
		key, _ := raw.(map[string]any)
		name, _ := key["name"].(string)
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// TestDataAPIKeys_ShownOnFormulasOverMCP checks which members see the Data API keys of a formula over MCP:
// get_formula, list_formulas and describe_bdb name a formula's keys to the owner and to an editor, a formula on no key
// carrying none, while a viewer sees no key at all.
func TestDataAPIKeys_ShownOnFormulasOverMCP(t *testing.T) {
	k := newKeyInstance(t)
	ownerID, owner := k.user("owner")
	editorID, editor := k.user("editor")
	viewerID, viewer := k.user("viewer")
	bdbID, formulas := k.bdbWithFormulas(owner, "Notes", "all-notes", "other-notes")
	k.createKey(owner, bdbID, "phone app", formulas[0])
	k.createKey(owner, bdbID, "web app", formulas[0])
	members := map[uuid.UUID]authz.BdbRole{editorID: authz.BdbRoleEditor, viewerID: authz.BdbRoleViewer}
	for member, role := range members {
		if _, err := k.inst.Bdbs.BdbSvc.AddMember(context.Background(), ownerID, uuid.MustParse(bdbID), member, role,
			[]authz.Role{authz.RoleUser}); err != nil {
			t.Fatalf("adding a %s: %v", role, err)
		}
	}

	for name, tt := range map[string]struct {
		token string
		want  []string
	}{
		"the owner": {token: owner, want: []string{"phone app", "web app"}},
		"an editor": {token: editor, want: []string{"phone app", "web app"}},
		"a viewer":  {token: viewer, want: []string{}},
	} {
		c := k.mcpSession(tt.token)
		wantOf := func(formula any) []string {
			if formula == "all-notes" {
				return tt.want
			}
			return []string{}
		}

		got := parseToolJSON(t, callTool(t, c, "get_formula", map[string]any{"bdb_id": bdbID, "ref": "all-notes"}))
		if names := sortedKeyNames(got); !slices.Equal(names, tt.want) {
			t.Errorf("get_formula's keys for %s = %v, want %v", name, names, tt.want)
		}
		for _, raw := range parseToolJSONArray(t, callTool(t, c, "list_formulas", map[string]any{"bdb_id": bdbID})) {
			f, _ := raw.(map[string]any)
			if names := sortedKeyNames(f); !slices.Equal(names, wantOf(f["name"])) {
				t.Errorf("list_formulas' keys of %v for %s = %v, want %v", f["name"], name, names, wantOf(f["name"]))
			}
		}
		overview := parseToolJSON(t, callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdbID}))
		described, _ := overview["formulas"].([]any)
		for _, raw := range described {
			f, _ := raw.(map[string]any)
			if names := sortedKeyNames(f); !slices.Equal(names, wantOf(f["name"])) {
				t.Errorf("describe_bdb's keys of %v for %s = %v, want %v", f["name"], name, names, wantOf(f["name"]))
			}
		}
		if len(described) != 2 {
			t.Errorf("describe_bdb's formulas for %s = %v, want both", name, described)
		}
	}
}
