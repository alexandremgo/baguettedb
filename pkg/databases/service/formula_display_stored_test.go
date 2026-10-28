package service

import (
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// TestUnmarshalDisplay_LegacyColumnFields covers the lazy read migration: configs stored before
// per-column entries carried flat title/body/check fields, which normalize into Columns on read.
func TestUnmarshalDisplay_LegacyColumnFields(t *testing.T) {
	legacy := `{"mode":"checklist","title_column":"item","check_column":"bought",` +
		`"slots":{"toggle":"flip-it"}}`
	d, err := unmarshalDisplay(legacy)
	if err != nil {
		t.Fatalf("unmarshalDisplay(legacy) error = %v", err)
	}
	if d.RoleColumn(domain.DisplayRoleTitle) != "item" || d.RoleColumn(domain.DisplayRoleCheck) != "bought" {
		t.Errorf("normalized columns = %+v, want title=item check=bought", d.Columns)
	}
	if d.Slots.Toggle != "flip-it" {
		t.Errorf("slots = %+v, want the toggle preserved", d.Slots)
	}

	// The new shape wins when both are present (a rewritten config never re-reads legacy keys).
	mixed := `{"mode":"cards","columns":[{"column":"name","role":"title"}],"title_column":"stale"}`
	d, err = unmarshalDisplay(mixed)
	if err != nil {
		t.Fatalf("unmarshalDisplay(mixed) error = %v", err)
	}
	if len(d.Columns) != 1 || d.RoleColumn(domain.DisplayRoleTitle) != "name" {
		t.Errorf("columns = %+v, want only the columns entry", d.Columns)
	}
}
