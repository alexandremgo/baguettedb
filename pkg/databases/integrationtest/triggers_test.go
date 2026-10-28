//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// initPlatformTables seeds a SQLite database at CurrentPlatformSchemaVersion
// (post-v4: unified _change_log + _change_snapshots + stacks). Shared by
// every integration test setup helper.
func initPlatformTables(t *testing.T, client *sqlitelocal.Client, databaseID string) {
	t.Helper()
	if err := service.InitPlatformSchema(context.Background(), client, databaseID); err != nil {
		t.Fatalf("initializing platform tables: %v", err)
	}
}
