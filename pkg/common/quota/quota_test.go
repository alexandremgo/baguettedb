package quota

import (
	"testing"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// fullLimits returns limits with every field set, so a test can tell "kept" from "zeroed" per field.
func fullLimits() Limits {
	return Limits{
		MaxBdbs:                 3,
		MaxDBSizeBytes:          10 << 20,
		MaxTotalDBSizeBytes:     100 << 20,
		MaxMembersPerBdb:        5,
		UndoStackMaxSize:        50,
		AffectedRowsMax:         200,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
		SnapshotMaxRows:         10000,
		MaxDBExportsPerWindow:   1,
		DBExportWindow:          24 * time.Hour,
		MaxFormulaExportRows:    100000,
	}
}

func TestFixed_NonAdminGetsConfiguredLimits(t *testing.T) {
	resolve := Fixed(fullLimits())
	for _, roles := range [][]authz.Role{{authz.RoleUser}, {}, nil} {
		if got := resolve(roles); got != fullLimits() {
			t.Errorf("Fixed(...)(%v) = %+v, want the configured limits unchanged", roles, got)
		}
	}
}

func TestFixed_AdminExemptFromOwnershipCaps(t *testing.T) {
	resolve := Fixed(fullLimits())
	admin := []authz.Role{authz.RoleUser, authz.RoleAdmin}

	want := fullLimits()
	want.MaxBdbs, want.MaxDBSizeBytes, want.MaxTotalDBSizeBytes = 0, 0, 0
	want.MaxMembersPerBdb, want.MaxDBExportsPerWindow = 0, 0
	if got := resolve(admin); got != want {
		t.Errorf("Fixed(...)(admin) = %+v\nwant ownership caps zeroed, everything else kept: %+v", got, want)
	}

	// Resolving for an admin must not alter what later callers see.
	if got := resolve([]authz.Role{authz.RoleUser}); got != fullLimits() {
		t.Errorf("after an admin resolution, Fixed(...)(user) = %+v, want the configured limits unchanged", got)
	}
}
