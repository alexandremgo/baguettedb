//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/filekek"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	dbapi "github.com/alexandremgo/baguettedb/pkg/databases/api"
	dbmcp "github.com/alexandremgo/baguettedb/pkg/databases/mcp"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// mcpShardPod is one databases pod exposing its MCP Streamable-HTTP endpoint (stateless) wrapped with the shard
// proxy, mirroring the production mount. It keeps the dbSvc + sqlite client so a test can seed an owned database
// on this pod's volume directly.
type mcpShardPod struct {
	dbSvc        *service.DatabaseService
	sqliteClient *sqlitelocal.Client
	baseURL      string // server root, used as a peer target (the proxy appends the original /internal/mcp path)
	mcpURL       string // full /internal/mcp endpoint for a client to dial
}

// newMCPShardPod builds a pod at the given ordinal. peerURL maps a shard ordinal to the base URL of its owner so
// this pod can forward; kek is shared so any pod can unwrap any database's DEK.
func newMCPShardPod(
	t *testing.T, color, ordinal int16, kek *filekek.Provider, peerURL func(color, ordinal int16) string,
) *mcpShardPod {
	t.Helper()
	dbRepo := newDatabaseRepo()
	sqliteClient, _ := newTestSQLiteClient(t, dbRepo, kek)
	classifier := sqlitelocal.NewClassifier()
	limits := quota.Fixed(quota.Limits{MaxDBSizeBytes: 10 * 1024 * 1024, MaxStatementsPerRequest: 100,
		MaxRowsPerRequest: 1000})
	dbSvc := service.NewDatabaseService(dbRepo, sqliteClient, sqliteClient, &allowAllBdbsApp{}, limits, kek, testLogger)
	dbSvc.SetShardOrdinal(ordinal)
	dbSvc.SetPodColor(color)
	querySvc := service.NewQueryService(dbSvc, sqliteClient, classifier, limits, 100, 0)
	skillSvc := service.NewSkillService(dbSvc, sqliteClient)
	formulaSvc := service.NewFormulaService(
		dbSvc, sqliteClient, classifier, querySvc, defaultTestFormulaRunTimeout, testLogger)

	tv, err := authhttp.NewTokenValidator(context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger)
	if err != nil {
		t.Fatalf("token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	shardRouter := dbapi.NewShardRouter(color, ordinal, peerURL, dbSvc, testLogger)
	mcpServer := dbmcp.NewServer(querySvc, skillSvc, formulaSvc, testLogger)
	// Stateless so a forwarded tools/call needs no prior session on the owner — the same option production uses.
	internalMCP := mcpserver.NewStreamableHTTPServer(mcpServer,
		mcpserver.WithHTTPContextFunc(mcputil.NewInternalHTTPContextFunc(tv)),
		mcpserver.WithStateLess(true),
	)

	mux := http.NewServeMux()
	mux.Handle("/internal/mcp", shardRouter.MCPProxy(1024*1024, internalMCP))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &mcpShardPod{
		dbSvc:        dbSvc,
		sqliteClient: sqliteClient,
		baseURL:      server.URL,
		mcpURL:       server.URL + "/internal/mcp",
	}
}

// mcpClient dials a pod's MCP endpoint with a forwarded bearer for the given user and completes the handshake.
func mcpClient(t *testing.T, mcpURL, bearer string) *mcpclient.Client {
	t.Helper()
	c, err := mcpclient.NewStreamableHttpClient(mcpURL,
		transport.WithHTTPHeaderFunc(func(context.Context) map[string]string {
			return map[string]string{"Authorization": "Bearer " + bearer}
		}),
	)
	if err != nil {
		t.Fatalf("creating MCP client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("starting MCP client: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "mcp-routing-test", Version: "0"},
		},
	}); err != nil {
		t.Fatalf("initializing MCP client: %v", err)
	}
	return c
}

// queryViaMCP runs execute_query for a bdb and returns the text content, failing on a tool error.
func queryViaMCP(t *testing.T, c *mcpclient.Client, bdbID uuid.UUID, sql string) string {
	t.Helper()
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "execute_query",
			Arguments: map[string]any{"bdb_id": bdbID.String(), "sql": sql},
		},
	})
	if err != nil {
		t.Fatalf("execute_query transport error: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("execute_query returned no content")
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("execute_query content[0] is %T, want TextContent", res.Content[0])
	}
	if res.IsError {
		t.Fatalf("execute_query is an error result: %s", text.Text)
	}
	return text.Text
}

// TestMCPShardRouting_ForwardsToOwnerAndServesLocal verifies the MCP analogue of the REST routing contract: an
// execute_query for a bdb owned by another pod, sent to a non-owner, is transparently proxied to the owner and
// returns rows; a query for a locally-owned bdb is served without forwarding. The bdb rides in the
// tools/call arguments (not the URL), so this exercises the body-peek proxy.
func TestMCPShardRouting_ForwardsToOwnerAndServesLocal(t *testing.T) {
	truncateAll(t)
	ctx := context.Background()
	kek := newTestKEKProvider(t)

	// pod1 owns shard 1; it never forwards in this test, so its peerURL is a placeholder.
	pod1 := newMCPShardPod(t, 0, 1, kek, func(int16, int16) string { return "http://unused" })
	// pod0 owns shard 0 and forwards shard-1 traffic to pod1's real server.
	pod0 := newMCPShardPod(t, 0, 0, kek, func(_, ordinal int16) string {
		if ordinal == 1 {
			return pod1.baseURL
		}
		return "http://unused"
	})

	// --- A bdb owned by shard 1, whose file lives on pod1 ---
	remoteBdb := uuid.New()
	remoteDB, err := pod1.dbSvc.CreateDatabase(ctx, remoteBdb) // creator-owns → shard 1, file on pod1
	if err != nil {
		t.Fatalf("creating remote database on pod1: %v", err)
	}
	for _, sql := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
		"INSERT INTO t (v) VALUES ('hello-remote')",
	} {
		if _, err := pod1.sqliteClient.Execute(ctx, remoteDB.String(), sql, nil); err != nil {
			t.Fatalf("seeding remote db (%q): %v", sql, err)
		}
	}

	// Query it via pod0's MCP endpoint — must be proxied to pod1 and return the row.
	user := uuid.New().String()
	client0 := mcpClient(t, pod0.mcpURL, userToken(t, user))
	if got := queryViaMCP(t, client0, remoteBdb, "SELECT v FROM t"); !strings.Contains(got, "hello-remote") {
		t.Fatalf("forwarded MCP query result = %q, want it to contain 'hello-remote'", got)
	}

	// --- A bdb owned by shard 0, whose file lives on pod0 ---
	localBdb := uuid.New()
	localDB, err := pod0.dbSvc.CreateDatabase(ctx, localBdb) // shard 0
	if err != nil {
		t.Fatalf("creating local database on pod0: %v", err)
	}
	for _, sql := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
		"INSERT INTO t (v) VALUES ('hello-local')",
	} {
		if _, err := pod0.sqliteClient.Execute(ctx, localDB.String(), sql, nil); err != nil {
			t.Fatalf("seeding local db (%q): %v", sql, err)
		}
	}

	// Served locally by pod0 (no forward needed).
	if got := queryViaMCP(t, client0, localBdb, "SELECT v FROM t"); !strings.Contains(got, "hello-local") {
		t.Fatalf("local MCP query result = %q, want it to contain 'hello-local'", got)
	}
}
