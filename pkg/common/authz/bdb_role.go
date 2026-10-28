package authz

import "slices"

// BdbRole represents a user's role within a bdb.
type BdbRole string

// The roles a member holds in a bdb.
const (
	BdbRoleOwner  BdbRole = "owner"
	BdbRoleEditor BdbRole = "editor"
	BdbRoleViewer BdbRole = "viewer"
)

// ValidBdbRoles contains all recognized bdb roles.
var ValidBdbRoles = []BdbRole{BdbRoleOwner, BdbRoleEditor, BdbRoleViewer}

// IsValidBdbRole checks whether the given role is a recognized bdb role.
func IsValidBdbRole(r BdbRole) bool {
	return slices.Contains(ValidBdbRoles, r)
}

// BdbPermission represents a permission within a bdb context.
type BdbPermission string

// The permissions a bdb role grants.
const (
	BdbPermManageMembers BdbPermission = "bdb:manage_members"
	BdbPermEdit          BdbPermission = "bdb:edit"
	BdbPermView          BdbPermission = "bdb:view"
)

// bdbRolePermissions maps each bdb role to its granted bdb permissions.
var bdbRolePermissions = map[BdbRole][]BdbPermission{
	BdbRoleOwner: {
		BdbPermManageMembers,
		BdbPermEdit,
		BdbPermView,
	},
	BdbRoleEditor: {
		BdbPermView,
	},
	BdbRoleViewer: {
		BdbPermView,
	},
}

// PermissionsForBdbRole returns the bdb permissions granted by the given bdb role.
func PermissionsForBdbRole(role BdbRole) []BdbPermission {
	return bdbRolePermissions[role]
}

// BdbRoleHasPermission checks whether the given bdb role grants the specified bdb permission.
func BdbRoleHasPermission(role BdbRole, perm BdbPermission) bool {
	return slices.Contains(bdbRolePermissions[role], perm)
}

// DatabasePermission represents a permission for database operations.
type DatabasePermission string

// The permissions on a bdb's database.
const (
	DBPermRead          DatabasePermission = "db:read"
	DBPermWrite         DatabasePermission = "db:write"
	DBPermManageSchema  DatabasePermission = "db:manage_schema"
	DBPermManageHistory DatabasePermission = "db:manage_history"
	DBPermExport        DatabasePermission = "db:export"
	DBPermImport        DatabasePermission = "db:import"
)

// ValidDatabasePermissions contains all recognized database permissions.
var ValidDatabasePermissions = []DatabasePermission{
	DBPermRead, DBPermWrite, DBPermManageSchema, DBPermManageHistory, DBPermExport, DBPermImport,
}

// IsValidDatabasePermission checks whether the given permission is a recognized database permission.
func IsValidDatabasePermission(p DatabasePermission) bool {
	return slices.Contains(ValidDatabasePermissions, p)
}

// databaseRolePermissions maps each bdb role to its granted database permissions.
// DBPermManageHistory grants the ability to undo / redo / revert changes made by
// other members in the bdb (undo_last_change_anyone et al.); it is owner-only.
// DBPermExport gates the full-database file export; also owner-only, because the
// exported file carries reserved platform tables (change-log attribution, undo
// stacks, skills) that DBPermRead holders cannot query.
// DBPermImport gates installing an exported file as the database, platform tables
// included; owner-only for the same reason.
var databaseRolePermissions = map[BdbRole][]DatabasePermission{
	BdbRoleOwner:  {DBPermRead, DBPermWrite, DBPermManageSchema, DBPermManageHistory, DBPermExport, DBPermImport},
	BdbRoleEditor: {DBPermRead, DBPermWrite},
	BdbRoleViewer: {DBPermRead},
}

// DatabasePermissionsForBdbRole returns the database permissions granted by the given bdb role.
func DatabasePermissionsForBdbRole(role BdbRole) []DatabasePermission {
	return databaseRolePermissions[role]
}

// BdbRoleHasDatabasePermission checks whether the given bdb role grants the specified database permission.
func BdbRoleHasDatabasePermission(role BdbRole, perm DatabasePermission) bool {
	return slices.Contains(databaseRolePermissions[role], perm)
}
