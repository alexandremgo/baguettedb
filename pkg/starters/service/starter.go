package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

var tracer = otel.Tracer("baguettedb/starters/service")

// StarterService is the apply engine: it turns an inert manifest into a live bdb by composing the bdbs and
// databases apps, acting as the creating user throughout.
type StarterService struct {
	registry *Registry
	bdbsApp  BdbsApp
	dbApp    DatabasesApp
	logger   zerolog.Logger
}

// NewStarterService creates a new StarterService.
func NewStarterService(registry *Registry, bdbsApp BdbsApp, dbApp DatabasesApp, logger zerolog.Logger) *StarterService {
	return &StarterService{registry: registry, bdbsApp: bdbsApp, dbApp: dbApp, logger: logger}
}

// ListStarters returns the merged starter catalog.
func (s *StarterService) ListStarters(ctx context.Context) ([]domain.StarterManifest, error) {
	return s.registry.List(ctx)
}

// CreateBdb creates a bdb for userID and returns its ID: seeded from the starter with the given key as
// CreateFromStarter does, or blank when starterKey is empty.
func (s *StarterService) CreateBdb(
	ctx context.Context, userID uuid.UUID, roles []authz.Role, starterKey, name, description string,
) (uuid.UUID, error) {
	if starterKey == "" {
		return s.bdbsApp.CreateBdb(ctx, userID, roles, name, description)
	}
	return s.CreateFromStarter(ctx, userID, roles, starterKey, name, description)
}

// GetBdb returns the bdb as userID, one of its members, sees it, in the shape a bdb creation answers with. A plain
// read, kept on the service so that the bdbs app stays reachable only through the app's services.
func (s *StarterService) GetBdb(ctx context.Context, userID, bdbID uuid.UUID) (apptypes.Bdb, error) {
	return s.bdbsApp.GetBdb(ctx, userID, bdbID)
}

// CreateFromStarter creates a new bdb seeded from the starter with the given key and returns the new bdb's ID.
// Empty name/description default to the manifest's. Returns domain.ErrStarterNotFound for an unknown key. On
// any seeding failure the freshly created bdb is deleted (best-effort) and the returned error wraps
// domain.ErrStarterApplyFailed together with the seeding cause, so the caller can safely retry the whole creation
// and still inspect what failed.
func (s *StarterService) CreateFromStarter(
	ctx context.Context, userID uuid.UUID, roles []authz.Role, starterKey, name, description string,
) (uuid.UUID, error) {
	ctx, span := tracer.Start(ctx, "StarterService.CreateFromStarter",
		trace.WithAttributes(
			attribute.String("user.id", userID.String()),
			attribute.String("starter.key", starterKey),
		),
	)
	defer span.End()

	manifest, err := s.registry.Get(ctx, starterKey)
	if err != nil {
		return uuid.Nil, err
	}

	if name == "" {
		name = manifest.Name
	}
	if description == "" {
		description = manifest.Description
	}

	bdbID, err := s.bdbsApp.CreateBdbWithProvenance(
		ctx, userID, roles, name, description, manifest.Key, manifest.Version)
	if err != nil {
		return uuid.Nil, fmt.Errorf("creating bdb from starter %q: %w", starterKey, err)
	}

	if err := s.apply(ctx, userID, bdbID, manifest); err != nil {
		s.cleanup(ctx, userID, bdbID, starterKey)
		return uuid.Nil, fmt.Errorf("%w: %q: %w", domain.ErrStarterApplyFailed, starterKey, err)
	}

	s.logger.Info().
		Str("bdb_id", bdbID.String()).
		Str("starter_key", manifest.Key).
		Int("starter_version", manifest.Version).
		Msg("Created bdb from starter")

	return bdbID, nil
}

// apply seeds the bdb from the manifest in dependency order: schema, then seed rows, then formulas (unattached
// ones first so an attached action can resolve its target's fresh ID), then display configs (once every slot
// ref exists), then skills.
func (s *StarterService) apply(ctx context.Context, userID, bdbID uuid.UUID, m domain.StarterManifest) error {
	if err := s.dbApp.ExecuteSchema(ctx, userID, bdbID, m.Schema); err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}
	for _, seed := range m.Seed {
		if err := s.dbApp.InsertRows(ctx, userID, bdbID, seed); err != nil {
			return fmt.Errorf("seeding table %q: %w", seed.Table, err)
		}
	}

	// Formulas are created in dependency order: unattached ones first, then children whose target
	// exists, and so on — attachment chains (a row-detail view's own slotted actions) settle in as
	// many passes as they are deep. A pass without progress means a dangling or cyclic target.
	createdIDs := make(map[string]uuid.UUID, len(m.Formulas))
	pending := m.Formulas
	for len(pending) > 0 {
		var stalled []domain.StarterFormula
		for _, f := range pending {
			targetID, ok := attachTargetID(createdIDs, f)
			if !ok {
				stalled = append(stalled, f)
				continue
			}
			id, err := s.dbApp.CreateFormula(ctx, userID, bdbID, f, targetID)
			if err != nil {
				return fmt.Errorf("creating formula %q: %w", f.Name, err)
			}
			createdIDs[f.Name] = id
		}
		if len(stalled) == len(pending) {
			return fmt.Errorf("creating formula %q: attached to %q which was never created",
				stalled[0].Name, stalled[0].AttachedTo)
		}
		pending = stalled
	}

	// Displays go last, once every formula exists: a display's slot refs name sibling formulas, and the
	// receiving service resolves them against the view's attached formulas (the same chicken-and-egg the
	// public API solves with its attach-then-slot authoring order).
	for _, f := range m.Formulas {
		if f.Display == nil {
			continue
		}
		if err := s.dbApp.UpdateFormulaDisplay(ctx, userID, bdbID, f.Name, *f.Display); err != nil {
			return fmt.Errorf("applying display of formula %q: %w", f.Name, err)
		}
	}

	for _, sk := range m.Skills {
		if err := s.dbApp.CreateSkill(ctx, userID, bdbID, sk); err != nil {
			return fmt.Errorf("creating skill %q: %w", sk.Name, err)
		}
	}

	return nil
}

// attachTargetID resolves a formula's AttachedTo slug to the ID its target received when it was created: a nil
// ID for an unattached formula, and ok = false while the target has not been created yet.
func attachTargetID(createdIDs map[string]uuid.UUID, f domain.StarterFormula) (*uuid.UUID, bool) {
	if f.AttachedTo == "" {
		return nil, true
	}

	if targetID, ok := createdIDs[f.AttachedTo]; ok {
		return &targetID, true
	}

	return nil, false
}

// cleanup best-effort deletes the partially seeded bdb and its database. A failed delete is logged loudly and
// the partial bdb is left in place — the bdb is seconds old with no user data, so no stronger guarantee is
// needed.
func (s *StarterService) cleanup(ctx context.Context, userID, bdbID uuid.UUID, starterKey string) {
	if err := s.bdbsApp.DeleteBdb(ctx, userID, bdbID); err != nil {
		s.logger.Error().Err(err).
			Str("bdb_id", bdbID.String()).
			Str("starter_key", starterKey).
			Msg("Failed to delete bdb after starter apply failure — leaving partial bdb")
		return
	}
	if err := s.dbApp.DeleteDatabase(ctx, bdbID); err != nil {
		s.logger.Error().Err(err).
			Str("bdb_id", bdbID.String()).
			Str("starter_key", starterKey).
			Msg("Failed to delete database after starter apply failure")
	}
}
