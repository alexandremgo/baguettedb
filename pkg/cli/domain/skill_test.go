package domain_test

import (
	"slices"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
)

// TestOrderSkills_BdbLevelFirst checks that the bdb-level skills come first, whether their table list is empty or
// nil, and that each group keeps its order.
func TestOrderSkills_BdbLevelFirst(t *testing.T) {
	skills := []apptypes.Skill{
		{Name: "triage", TargetTables: []string{"tasks"}},
		{Name: "orientation", TargetTables: []string{}},
		{Name: "naming", TargetTables: []string{"tasks", "lists"}},
		{Name: "tone"},
	}

	domain.OrderSkills(skills)

	got := make([]string, len(skills))
	for i, s := range skills {
		got[i] = s.Name
	}
	if want := []string{"orientation", "tone", "triage", "naming"}; !slices.Equal(got, want) {
		t.Errorf("OrderSkills = %v, want %v", got, want)
	}
}
