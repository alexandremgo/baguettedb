//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestRun_ServeMembers checks members end to end on a local instance served by bdb serve, each person with a personal
// access token of their own: the owner invites bob to a bdb as an editor, the invite's link pointing at the served
// web app; bob reads the invite and accepts it with his token; over MCP, bob then sees the bdb with the editor role
// and writes to it; on the serving machine, bob, acted as with --as, has the bdb too, and the owner's change history
// names him as the author of his write.
func TestRun_ServeMembers(t *testing.T) {
	clearDataDirEnv(t)
	dataDir := filepath.Join(t.TempDir(), ".baguettedb")
	bdb := func(stdin string, args ...string) string {
		t.Helper()
		code, stdout, stderr := runCLIWithStdin(t, stdin, append([]string{"bdb", "--data-dir", dataDir}, args...)...)
		if code != exitOK {
			t.Fatalf("%v = %d %q, stderr %q", args, code, stdout, stderr)
		}
		return stdout
	}
	bdb("bob-password-1\n", "users", "create", "bob@example.com", "--password-stdin")
	ownerToken := strings.TrimSpace(bdb("", "tokens", "create", "owner-laptop"))
	bobToken := strings.TrimSpace(bdb("", "--as", "bob@example.com", "tokens", "create", "bob-laptop"))
	var kitchen struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(bdb("", "bdbs", "create", "Kitchen", "--json")), &kitchen); err != nil {
		t.Fatal(err)
	}
	bdb("", "sql", "schema", "Kitchen", "CREATE TABLE items (name TEXT)")

	baseURL, _, stop := startServe(t, dataDir)
	defer stop()

	status, body := withBearer(t, http.MethodPost, baseURL+"/api/bdbs/v1/bdbs/"+kitchen.ID+"/invites", ownerToken,
		map[string]string{"role": "editor"})
	var invite struct {
		Token     string `json:"token"`
		InviteURL string `json:"invite_url"`
	}
	if err := json.Unmarshal(body, &invite); status != http.StatusCreated || err != nil ||
		invite.InviteURL != baseURL+"/invite/"+invite.Token {
		t.Fatalf("creating the invite = %d %s (%v); want 201 and a link to %s/invite/<token>", status, body, err,
			baseURL)
	}
	if status, body := withBearer(t, http.MethodGet, baseURL+"/api/bdbs/v1/invites/"+invite.Token, bobToken,
		nil); status != http.StatusOK || !strings.Contains(string(body), "Kitchen") {
		t.Errorf("reading the invite as bob = %d %s, want 200 and the bdb's name", status, body)
	}
	if status, body := withBearer(t, http.MethodPost, baseURL+"/api/bdbs/v1/invites/"+invite.Token+"/accept",
		bobToken, nil); status != http.StatusOK {
		t.Fatalf("accepting the invite as bob = %d %s, want 200", status, body)
	}

	c, _ := servedMCP(t, baseURL, bobToken)
	bdbs := callToolJSON[[]map[string]any](t, c, "list_bdbs", nil)
	if len(bdbs) != 1 || bdbs[0]["id"] != kitchen.ID || bdbs[0]["role"] != "editor" {
		t.Errorf("list_bdbs as bob = %v, want Kitchen alone, as an editor", bdbs)
	}
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "execute_data",
		Arguments: map[string]any{"bdb_id": kitchen.ID, "sql": "INSERT INTO items (name) VALUES ('flour')"},
	}})
	if err != nil || res.IsError {
		t.Errorf("execute_data as bob = %v, %v; want the row inserted", res, err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("closing the MCP client: %v", err)
	}

	if listed := bdb("", "--as", "bob@example.com", "bdbs"); !hasRowPrefix(strings.Split(listed, "\n"),
		[]string{"Kitchen", kitchen.ID, "editor"}) {
		t.Errorf("bdbs --as bob = %q, want Kitchen as an editor", listed)
	}
	if described := bdb("", "--as", "bob@example.com", "bdbs", "describe", "Kitchen"); !strings.Contains(described,
		"Kitchen  (editor)\n") || !strings.Contains(described, "\nOwner:        ") {
		t.Errorf("bdbs describe --as bob = %q, want bob's role and the bdb's owner", described)
	}
	if code, _, stderr := runCLI(t, "bdb", "--data-dir", dataDir, "--as", "bob@example.com", "bdbs", "update",
		"Kitchen", "--name", "Bob's kitchen"); code != exitFailure || !strings.Contains(stderr, "only the owner") {
		t.Errorf("bdbs update --as bob = %d, stderr %q; want an editor refused", code, stderr)
	}
	if changes := bdb("", "changes", "Kitchen"); !strings.Contains(changes, "  bob@example.com  ") {
		t.Errorf("changes of Kitchen as the owner = %q, want bob's write under his email", changes)
	}
}

// withBearer sends method to target with token as its bearer token and body as JSON (none when nil), and returns the
// status and the response body.
func withBearer(t *testing.T, method, target, token string, body any) (int, []byte) {
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
	req.Header.Set("Authorization", "Bearer "+token)
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
