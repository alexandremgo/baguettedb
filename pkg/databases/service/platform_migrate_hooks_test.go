package service

import (
	"context"
	"testing"
)

// TestPostMigrationHook_AllVersionsAreNoOps pins that no platform version currently registers Go-side
// post-migration work: the hook seam exists for future migrations, and it must stay silent until one uses it.
func TestPostMigrationHook_AllVersionsAreNoOps(t *testing.T) {
	for _, version := range []int{0, 1, 4, CurrentPlatformSchemaVersion, 99} {
		exec := &fakePlatformExecutor{}
		if err := postMigrationHook(context.Background(), exec, "ns", version); err != nil {
			t.Fatalf("version %d: unexpected error: %v", version, err)
		}
		if len(exec.executed) != 0 {
			t.Errorf("version %d: hook executed %d statements, want 0: %v",
				version, len(exec.executed), exec.executed)
		}
	}
}
