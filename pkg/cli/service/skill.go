package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// skillReader reads a bdb's skills in the databases app.
type skillReader interface {
	// ListSkills returns every skill of the bdb bdbID, with its body.
	ListSkills(ctx context.Context, bdbID uuid.UUID) ([]apptypes.Skill, error)

	// GetSkill returns the skill ref (a name or an ID) of the bdb bdbID. An unknown skill wraps
	// domain.ErrSkillNotFound.
	GetSkill(ctx context.Context, bdbID uuid.UUID, ref string) (apptypes.Skill, error)
}

// skillWriter creates and changes a bdb's skills in the databases app.
type skillWriter interface {
	// CreateSkill creates a skill in the bdb bdbID targeting tables, a bdb-level skill when tables is empty, and
	// returns it.
	CreateSkill(
		ctx context.Context, bdbID uuid.UUID, name, description, body string, tables []string,
	) (apptypes.Skill, error)

	// UpdateSkill applies change to the skill ref (a name or an ID) of the bdb bdbID and returns it. An unknown skill
	// wraps domain.ErrSkillNotFound.
	UpdateSkill(ctx context.Context, bdbID uuid.UUID, ref string, change domain.SkillChange) (apptypes.Skill, error)
}

// SkillService runs the skill use cases: listing, showing, creating and changing.
type SkillService struct {
	bdbs   bdbResolver
	reader skillReader
	writer skillWriter
}

// NewSkillService creates a SkillService resolving bdbs with bdbs and reading and writing skills in the databases
// app.
func NewSkillService(bdbs bdbResolver, reader skillReader, writer skillWriter) *SkillService {
	return &SkillService{bdbs: bdbs, reader: reader, writer: writer}
}

// List returns the skills of the bdb ref names.
func (s *SkillService) List(ctx context.Context, ref string) (domain.Bdb, []apptypes.Skill, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, nil, err
	}
	skills, err := s.reader.ListSkills(ctx, b.ID)
	if err != nil {
		return b, nil, fmt.Errorf("in %s: listing skills: %w", b.Name, err)
	}
	return b, skills, nil
}

// Show returns the skill name of the bdb ref names.
func (s *SkillService) Show(ctx context.Context, ref, name string) (domain.Bdb, apptypes.Skill, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, apptypes.Skill{}, err
	}
	skill, err := s.reader.GetSkill(ctx, b.ID, name)
	if err != nil {
		return b, apptypes.Skill{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, skill, nil
}

// Create creates the skill name in the bdb ref names, targeting tables, or the whole bdb when tables is empty.
func (s *SkillService) Create(
	ctx context.Context, ref, name, description, body string, tables []string,
) (domain.Bdb, apptypes.Skill, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, apptypes.Skill{}, err
	}
	skill, err := s.writer.CreateSkill(ctx, b.ID, name, description, body, tables)
	if err != nil {
		return b, apptypes.Skill{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, skill, nil
}

// Update applies change to the skill name of the bdb ref names.
func (s *SkillService) Update(
	ctx context.Context, ref, name string, change domain.SkillChange,
) (domain.Bdb, apptypes.Skill, error) {
	b, err := s.bdbs.Resolve(ctx, ref)
	if err != nil {
		return domain.Bdb{}, apptypes.Skill{}, err
	}
	skill, err := s.writer.UpdateSkill(ctx, b.ID, name, change)
	if err != nil {
		return b, apptypes.Skill{}, fmt.Errorf("in %s: %w", b.Name, err)
	}
	return b, skill, nil
}
