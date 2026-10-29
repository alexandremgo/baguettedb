package domain

import (
	"cmp"
	"errors"
	"slices"

	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// SkillChange holds the fields of a skill to change; a nil field is left as it is. Tables replaces the tables the
// skill targets, and an empty list makes it a bdb-level skill.
type SkillChange struct {
	Name        *string
	Description *string
	Body        *string
	Tables      *[]string
}

// IsEmpty reports whether the change sets no field.
func (c SkillChange) IsEmpty() bool {
	return c.Name == nil && c.Description == nil && c.Body == nil && c.Tables == nil
}

// ErrSkillNotFound reports a skill reference that matches no skill in the bdb.
var ErrSkillNotFound = errors.New("no skill")

// OrderSkills orders skills in place for an agent to read: the bdb-level ones first, which explain the bdb as a whole,
// then the ones targeting tables, each group keeping its order.
func OrderSkills(skills []apptypes.Skill) {
	slices.SortStableFunc(skills, func(a, b apptypes.Skill) int {
		return cmp.Compare(min(len(a.TargetTables), 1), min(len(b.TargetTables), 1))
	})
}
