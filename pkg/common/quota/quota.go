// Package quota holds the resource limits that apply to a user, resolved from their global roles: Limits gathers the
// caps, a zero cap being off or the built-in default, and a Resolver maps roles to Limits. Fixed applies the same
// limits to everyone, admins exempt from the ownership caps; a downstream distribution supplies role-dependent ones.
package quota

import (
	"slices"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// Resolver resolves the resource limits that apply to a user from their global roles. Downstream distributions
// register role-dependent resolvers; Fixed is the flat default.
type Resolver func(roles []authz.Role) Limits

// Limits holds the resource quotas that apply to one user. Every field is a cap; 0 means the cap is off
// (unlimited) except where noted, so an unset field never blocks anything by accident.
type Limits struct {
	// MaxBdbs caps the bdbs a user may own; 0 = unlimited.
	MaxBdbs int
	// MaxDBSizeBytes caps one bdb database's on-disk size; 0 = unlimited.
	MaxDBSizeBytes int64
	// MaxTotalDBSizeBytes caps the aggregate size of every database a user owns; 0 = unlimited.
	MaxTotalDBSizeBytes int64
	// MaxMembersPerBdb caps a bdb's member count, owner included; 0 = unlimited.
	MaxMembersPerBdb int
	// UndoStackMaxSize caps the retained undo entries per user and database; 0 = the built-in default.
	UndoStackMaxSize int
	// AffectedRowsMax caps the affected rows echoed back in a mutation response; 0 = the built-in default.
	AffectedRowsMax int
	// MaxStatementsPerRequest caps the statements accepted in one execute request; 0 = unlimited.
	MaxStatementsPerRequest int
	// MaxRowsPerRequest caps the cumulative rows a mutating request may touch; 0 = unlimited.
	MaxRowsPerRequest int
	// MaxQueryResultRows caps the rows one read request may return, on the direct query path and formula runs
	// alike; 0 = unlimited. Keep it above the largest formula page (200 rows plus one over-fetched) so paginated
	// views keep working.
	MaxQueryResultRows int
	// MaxConcurrentQueries caps the SQL requests one user may have in flight at the same time in one process;
	// 0 = unlimited.
	MaxConcurrentQueries int
	// SnapshotMaxRows caps the rows captured per destructive-DDL pre-image; 0 = the built-in default.
	SnapshotMaxRows int
	// MaxDBExportsPerWindow caps successful full-database exports per database and user within
	// DBExportWindow; 0 = unlimited.
	MaxDBExportsPerWindow int
	// DBExportWindow is the rolling window over which MaxDBExportsPerWindow is counted.
	DBExportWindow time.Duration
	// MaxFormulaExportRows caps the rows one formula CSV export may return; 0 = unlimited.
	MaxFormulaExportRows int
}

// Fixed returns a Resolver that applies the same limits to everyone, except that admins are exempt from the
// ownership caps: for a role set containing authz.RoleAdmin, MaxBdbs, MaxDBSizeBytes, MaxTotalDBSizeBytes,
// MaxMembersPerBdb, and MaxDBExportsPerWindow resolve to 0 (unlimited) while every other field keeps its
// configured value. Each call resolves from the limits as configured: an admin resolution never changes what
// later calls see.
func Fixed(l Limits) Resolver {
	return func(roles []authz.Role) Limits {
		// Copies first: l is the closure's captured variable, shared by every call.
		out := l
		if slices.Contains(roles, authz.RoleAdmin) {
			out.MaxBdbs, out.MaxDBSizeBytes, out.MaxTotalDBSizeBytes = 0, 0, 0
			out.MaxMembersPerBdb, out.MaxDBExportsPerWindow = 0, 0
		}
		return out
	}
}
