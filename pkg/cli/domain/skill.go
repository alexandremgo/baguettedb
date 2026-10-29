package domain

import "errors"

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
