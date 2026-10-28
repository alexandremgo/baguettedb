package domain

import (
	"reflect"
	"strings"
	"testing"
)

// validManifest builds a structurally valid manifest exercising every component: schema, a view, an action
// attached to it, and a skill. Tests mutate one aspect at a time.
func validManifest(key string) StarterManifest {
	return StarterManifest{
		Key:         key,
		Version:     1,
		Name:        "Test starter " + key,
		Description: "A starter used in validation tests.",
		Schema:      []string{"CREATE TABLE things (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"},
		Formulas: []StarterFormula{
			{
				Name:        "things-list",
				Description: "Lists all things.",
				SQL:         "SELECT id AS rowid, name FROM things ORDER BY id",
			},
			{
				Name:        "delete-thing",
				Description: "Deletes the selected thing.",
				SQL:         "DELETE FROM things WHERE id = :rowid",
				Params:      []StarterFormulaParam{{Name: "rowid", Type: "integer", Source: "selection-single"}},
				AttachedTo:  "things-list",
			},
		},
		Skills: []StarterSkill{
			{
				Name:        "things-orientation",
				Description: "How to work with things.",
				Body:        "Keep thing names unique.",
			},
		},
	}
}

// TestValidateManifestSet pins the structural validation contract: each case mutates one aspect of a valid set
// and expects an error carrying the offending identifier.
func TestValidateManifestSet(t *testing.T) {
	tests := []struct {
		name      string
		manifests func() []StarterManifest
		// wantErrContaining is a substring the error must carry (typically the offending identifier, so
		// authoring errors are actionable); empty means the set must validate cleanly.
		wantErrContaining string
	}{
		{
			name: "valid set",
			manifests: func() []StarterManifest {
				return []StarterManifest{validManifest("alpha"), validManifest("beta")}
			},
		},
		{
			name: "seed for a table the schema does not create",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Seed = []StarterSeed{{Table: "kinds", Columns: []string{"name"}, Rows: [][]any{{"a"}}}}
				return []StarterManifest{m}
			},
			wantErrContaining: "kinds",
		},
		{
			name: "seed row narrower than its columns",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Seed = []StarterSeed{{Table: "things", Columns: []string{"id", "name"}, Rows: [][]any{{1, "a"}, {2}}}}
				return []StarterManifest{m}
			},
			wantErrContaining: "row 1",
		},
		{
			name: "seed without columns",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Seed = []StarterSeed{{Table: "things", Rows: [][]any{{"a"}}}}
				return []StarterManifest{m}
			},
			wantErrContaining: "columns",
		},
		{
			name: "seed value that is not a scalar",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Seed = []StarterSeed{{Table: "things", Columns: []string{"name"}, Rows: [][]any{{[]string{"a"}}}}}
				return []StarterManifest{m}
			},
			wantErrContaining: "scalar",
		},
		{
			name: "seed of a quoted, comment-led table",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Schema = append(m.Schema,
					"-- The kinds a thing can be.\nCREATE TABLE IF NOT EXISTS \"kinds\" (name TEXT)")
				m.Seed = []StarterSeed{{Table: "kinds", Columns: []string{"name"}, Rows: [][]any{{"a"}, {nil}}}}
				return []StarterManifest{m}
			},
		},
		{
			name: "duplicate key across manifests",
			manifests: func() []StarterManifest {
				return []StarterManifest{validManifest("alpha"), validManifest("alpha")}
			},
			wantErrContaining: "alpha",
		},
		{
			name: "empty key",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Key = ""
				return []StarterManifest{m}
			},
			wantErrContaining: "key",
		},
		{
			name: "missing version",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Version = 0
				return []StarterManifest{m}
			},
			wantErrContaining: "version",
		},
		{
			name: "negative version",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Version = -3
				return []StarterManifest{m}
			},
			wantErrContaining: "version",
		},
		{
			name: "empty name",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Name = ""
				return []StarterManifest{m}
			},
			wantErrContaining: "name",
		},
		{
			name: "no schema statements",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Schema = nil
				return []StarterManifest{m}
			},
			wantErrContaining: "schema",
		},
		{
			name: "blank schema statement",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Schema = append(m.Schema, "  \n\t")
				return []StarterManifest{m}
			},
			wantErrContaining: "schema",
		},
		{
			name: "duplicate formula names within a manifest",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				dup := m.Formulas[0]
				m.Formulas = append(m.Formulas, dup)
				return []StarterManifest{m}
			},
			wantErrContaining: "things-list",
		},
		{
			name: "duplicate skill names within a manifest",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Skills = append(m.Skills, m.Skills[0])
				return []StarterManifest{m}
			},
			wantErrContaining: "things-orientation",
		},
		{
			name: "attached_to references an unknown formula",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Formulas[1].AttachedTo = "no-such-view"
				return []StarterManifest{m}
			},
			wantErrContaining: "no-such-view",
		},
		{
			name: "attached_to references the formula itself",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Formulas[1].AttachedTo = "delete-thing"
				return []StarterManifest{m}
			},
			wantErrContaining: "delete-thing",
		},
		{
			// A chain is valid: a row-detail view's own slotted actions attach to the detail, which is
			// itself attached to the list view.
			name: "attachment chain resolves",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Formulas = append(m.Formulas, StarterFormula{
					Name:        "archive-thing",
					Description: "Archives the selected thing.",
					SQL:         "UPDATE things SET name = name WHERE id = :rowid",
					Params:      []StarterFormulaParam{{Name: "rowid", Type: "integer", Source: "selection-single"}},
					AttachedTo:  "delete-thing",
				})
				return []StarterManifest{m}
			},
		},
		{
			name: "attachment cycle",
			manifests: func() []StarterManifest {
				m := validManifest("alpha")
				m.Formulas = append(m.Formulas,
					StarterFormula{
						Name:        "thing-a",
						Description: "One half of a cycle.",
						SQL:         "SELECT id AS rowid FROM things WHERE id = :rowid",
						Params: []StarterFormulaParam{
							{Name: "rowid", Type: "integer", Source: "selection-single"},
						},
						AttachedTo: "thing-b",
					},
					StarterFormula{
						Name:        "thing-b",
						Description: "The other half of a cycle.",
						SQL:         "SELECT id AS rowid FROM things WHERE id = :rowid",
						Params: []StarterFormulaParam{
							{Name: "rowid", Type: "integer", Source: "selection-single"},
						},
						AttachedTo: "thing-a",
					},
				)
				return []StarterManifest{m}
			},
			wantErrContaining: "cycle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateManifestSet(tt.manifests())
			if tt.wantErrContaining == "" {
				if err != nil {
					t.Fatalf("ValidateManifestSet() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateManifestSet() = nil, want error containing %q", tt.wantErrContaining)
			}
			if !strings.Contains(err.Error(), tt.wantErrContaining) {
				t.Fatalf("ValidateManifestSet() = %q, want error containing %q", err, tt.wantErrContaining)
			}
		})
	}
}

// TestValidateManifestSet_FormulaDisplays pins the display-ref rule: every slot ref — fixed slots and the
// bulk / row_actions lists alike — must name a formula in the same manifest attached to the view carrying the
// display.
func TestValidateManifestSet_FormulaDisplays(t *testing.T) {
	// withDisplay builds a manifest whose view carries the given display, alongside its attached delete-thing
	// action (from validManifest), an attached archive-thing action, and a top-level loose-action.
	withDisplay := func(display *StarterFormulaDisplay) []StarterManifest {
		m := validManifest("alpha")
		m.Formulas = append(m.Formulas,
			StarterFormula{
				Name:        "archive-thing",
				Description: "Archives the selected things.",
				SQL:         "UPDATE things SET name = name WHERE id IN (:rowids)",
				Params:      []StarterFormulaParam{{Name: "rowids", Type: "integer", Source: "selection-bulk"}},
				AttachedTo:  "things-list",
			},
			StarterFormula{
				Name:        "loose-action",
				Description: "A top-level action attached to nothing.",
				SQL:         "DELETE FROM things WHERE name = :name",
				Params:      []StarterFormulaParam{{Name: "name", Type: "text", Source: "prompt"}},
			},
		)
		m.Formulas[0].Display = display
		return []StarterManifest{m}
	}

	tests := []struct {
		name              string
		display           *StarterFormulaDisplay
		wantErrContaining string
	}{
		{
			name: "valid slots and lists",
			display: &StarterFormulaDisplay{
				Mode: "table",
				Slots: StarterDisplaySlots{
					Delete: "delete-thing",
					Bulk:   []string{"archive-thing"},
				},
			},
		},
		{
			name: "slot ref does not exist",
			display: &StarterFormulaDisplay{
				Mode:  "table",
				Slots: StarterDisplaySlots{Delete: "no-such-action"},
			},
			wantErrContaining: "no-such-action",
		},
		{
			name: "slot ref not attached to the view",
			display: &StarterFormulaDisplay{
				Mode:  "table",
				Slots: StarterDisplaySlots{Delete: "loose-action"},
			},
			wantErrContaining: "loose-action",
		},
		{
			name: "list entry not attached to the view",
			display: &StarterFormulaDisplay{
				Mode:  "table",
				Slots: StarterDisplaySlots{Bulk: []string{"loose-action"}},
			},
			wantErrContaining: "loose-action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateManifestSet(withDisplay(tt.display))
			if tt.wantErrContaining == "" {
				if err != nil {
					t.Fatalf("ValidateManifestSet() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateManifestSet() = nil, want error containing %q", tt.wantErrContaining)
			}
			if !strings.Contains(err.Error(), tt.wantErrContaining) {
				t.Fatalf("ValidateManifestSet() = %q, want error containing %q", err, tt.wantErrContaining)
			}
		})
	}
}

// TestStarterSeed_InsertStatement pins the rendered INSERT: quoted identifiers, one placeholder group per row,
// and the values flattened in row order.
func TestStarterSeed_InsertStatement(t *testing.T) {
	seed := StarterSeed{
		Table:   "meal slots",
		Columns: []string{"name", "position"},
		Rows:    [][]any{{"breakfast", 1}, {"dinner", nil}},
	}
	sql, args := seed.InsertStatement()
	wantSQL := `INSERT INTO "meal slots" ("name", "position") VALUES (?, ?), (?, ?)`
	if sql != wantSQL {
		t.Errorf("InsertStatement() sql = %q, want %q", sql, wantSQL)
	}
	if want := []any{"breakfast", 1, "dinner", nil}; !reflect.DeepEqual(args, want) {
		t.Errorf("InsertStatement() args = %v, want %v", args, want)
	}
}
