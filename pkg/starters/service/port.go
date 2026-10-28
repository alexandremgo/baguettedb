package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

// StarterSource is an outbound port supplying starter manifests from one backend; several sources serve the catalog
// side by side. A source must serve only manifests that pass domain.ValidateManifestSet, checked where its data enters
// the system — consumers do not re-validate on the read path.
// Exported so that SetupOptions.Sources can name it.
type StarterSource interface {
	// List returns every manifest the source offers.
	List(ctx context.Context) ([]domain.StarterManifest, error)
	// Get returns the manifest with the given key, or domain.ErrStarterNotFound.
	Get(ctx context.Context, key string) (domain.StarterManifest, error)
}

// BdbsApp defines the subset of the bdbs app needed by starters. Its creations fail with domain.ErrBdbNameEmpty,
// domain.ErrBdbNameTooLong, domain.ErrBdbNameTaken or domain.BdbLimitReachedError when the new bdb breaks one of the
// bdbs app's rules.
// Exported so that setup.go can accept it as a parameter.
type BdbsApp interface {
	// CreateBdb creates a blank bdb owned by the given user and returns its ID.
	CreateBdb(ctx context.Context, userID uuid.UUID, roles []authz.Role, name, description string) (uuid.UUID, error)

	// CreateBdbWithProvenance creates a bdb owned by the given user, recording which starter (and version) it
	// was created from, and returns the new bdb's ID. Enforces the same quota rules as a blank creation.
	CreateBdbWithProvenance(ctx context.Context, userID uuid.UUID, roles []authz.Role,
		name, description, starterKey string, starterVersion int) (uuid.UUID, error)

	// GetBdb returns the bdb as the given user, one of its members, sees it.
	GetBdb(ctx context.Context, userID, bdbID uuid.UUID) (apptypes.Bdb, error)

	// DeleteBdb soft-deletes the bdb. Only the owner can delete.
	DeleteBdb(ctx context.Context, userID, bdbID uuid.UUID) error
}

// DatabasesApp defines the subset of the databases app needed by starters. Every method acts as the given
// user, so seeding runs through the same validation, permission, and audit paths as a hand-built bdb.
// Exported so that setup.go can accept it as a parameter.
type DatabasesApp interface {
	// ExecuteSchema runs the DDL statements against the bdb's database with schema intent.
	ExecuteSchema(ctx context.Context, userID, bdbID uuid.UUID, statements []string) error

	// InsertRows inserts one seed's rows into its table as a single data-intent statement, so the rows land
	// in the change log like any member's insert would.
	InsertRows(ctx context.Context, userID, bdbID uuid.UUID, seed domain.StarterSeed) error

	// CreateFormula creates one formula and returns its new ID. attachedToID is the resolved ID of the view an
	// action attaches to (nil for a top-level formula); the formula's AttachedTo slug is not interpreted here.
	// The formula's Display is not applied here either — slot refs can only resolve once every sibling exists.
	CreateFormula(ctx context.Context, userID, bdbID uuid.UUID,
		formula domain.StarterFormula, attachedToID *uuid.UUID) (uuid.UUID, error)

	// UpdateFormulaDisplay stores the formula's display config, replacing any existing one. Slot refs may be
	// sibling formula names — they resolve against the view's attached formulas where the update runs.
	UpdateFormulaDisplay(ctx context.Context, userID, bdbID uuid.UUID, ref string,
		display domain.StarterFormulaDisplay) error

	// CreateSkill creates one skill.
	CreateSkill(ctx context.Context, userID, bdbID uuid.UUID, skill domain.StarterSkill) error

	// DeleteDatabase removes the bdb's database. Idempotent: returns nil if no database exists for the bdb.
	DeleteDatabase(ctx context.Context, bdbID uuid.UUID) error
}
