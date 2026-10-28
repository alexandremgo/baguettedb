package domain

import (
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// Platform-managed table names backing the skills system.
const (
	SkillsTable       = "_skills"
	SkillTargetsTable = "_skill_targets"
)

// SkillBodyMaxBytes caps the size of a skill body to keep agent context lean. Skill bodies are inlined into
// LLM context during bdb orientation, so their size taxes every future agent interaction in the bdb.
const SkillBodyMaxBytes = 100 * 1024

// SkillNameMaxLen caps the human-readable skill name. Names are slug-shaped
// and used to address skills by reference in MCP and REST.
const SkillNameMaxLen = 64

// SkillDescriptionMaxLen caps the skill description that appears alongside the name in listing payloads.
// Calibrated against real-world LLM tool/skill descriptions: enough headroom for "TRIGGER when X / SKIP if Y"
// routing rules without letting users write essays here (the body is for that).
const SkillDescriptionMaxLen = 2000

// skillNamePattern matches lowercase slug names: must start with a letter,
// followed by lowercase letters, digits, or hyphens. Mirrors Claude Code skill
// naming conventions (e.g. "update-config", "keybindings-help").
var skillNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Skill is an agent-authored markdown instruction stored per-bdb.
// A skill can target 0..N tables: zero targets makes it a bdb-level instruction, table targets mark which
// tables it documents. Targets are a relevance hint — delivery surfaces inline every body during bdb
// orientation, and additionally group table-targeted bodies with their table's schema.
type Skill struct {
	ID           uuid.UUID
	Name         string
	Description  string
	Body         string
	TargetTables []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ValidateSkillName checks that name is a non-empty slug matching skillNamePattern
// and within SkillNameMaxLen.
func ValidateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrSkillNameInvalid)
	}
	if len(name) > SkillNameMaxLen {
		return fmt.Errorf("%w: name must be at most %d characters", ErrSkillNameInvalid, SkillNameMaxLen)
	}
	if !skillNamePattern.MatchString(name) {
		return fmt.Errorf("%w: name must match %s", ErrSkillNameInvalid, skillNamePattern.String())
	}
	return nil
}

// ValidateSkillDescription checks that the description is non-empty and within
// SkillDescriptionMaxLen.
func ValidateSkillDescription(description string) error {
	if description == "" {
		return fmt.Errorf("%w: description must not be empty", ErrSkillDescriptionInvalid)
	}
	if len(description) > SkillDescriptionMaxLen {
		return fmt.Errorf("%w: description must be at most %d characters",
			ErrSkillDescriptionInvalid, SkillDescriptionMaxLen)
	}
	return nil
}

// ValidateSkillBody checks that body is non-empty and within SkillBodyMaxBytes.
func ValidateSkillBody(body string) error {
	if body == "" {
		return fmt.Errorf("%w: body must not be empty", ErrSkillBodyInvalid)
	}
	if len(body) > SkillBodyMaxBytes {
		return fmt.Errorf("%w: body must be at most %d bytes (got %d)",
			ErrSkillBodyTooLarge, SkillBodyMaxBytes, len(body))
	}
	return nil
}
