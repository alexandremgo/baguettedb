package embedded

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
)

const fixtureManifest = `key: fixture
version: 2
name: Fixture starter
description: A starter loaded from a fixture filesystem.
icon: "🧪"
tags: [testing]
schema:
  - CREATE TABLE samples (id INTEGER PRIMARY KEY, label TEXT NOT NULL)
seed:
  - table: samples
    columns: [id, label]
    rows:
      - [1, first]
      - [2, second]
formulas:
  - name: samples-list
    label: All samples
    description: Lists samples, optionally filtered to one label.
    sql: SELECT id AS rowid, label FROM samples WHERE (:label IS NULL OR label = :label) ORDER BY id
    params:
      - name: label
        type: text
        source: lookup
        optional: true
        lookup:
          table: samples
          label_column: label
          value_column: label
  - name: delete-sample
    description: Deletes the selected sample.
    sql: DELETE FROM samples WHERE id = :rowid
    params:
      - name: rowid
        type: integer
        source: selection-single
    attached_to: samples-list
skills:
  - name: samples-orientation
    description: How to work with samples.
    body: Keep sample labels short.
    targets: [samples]
`

// TestNewSourceFromFS decodes a fixture file through the same loader as the embedded source and verifies the
// DTO-to-domain conversion of every field family: metadata, schema, params (with lookup), attachment, targets.
func TestNewSourceFromFS(t *testing.T) {
	fsys := fstest.MapFS{
		"builtin/fixture.yaml": &fstest.MapFile{Data: []byte(fixtureManifest)},
	}

	src, err := newSourceFromFS(fsys, "builtin/*.yaml")
	if err != nil {
		t.Fatalf("newSourceFromFS() error = %v", err)
	}

	manifests, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("List() returned %d manifests, want 1", len(manifests))
	}

	m, err := src.Get(t.Context(), "fixture")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if m.Version != 2 || m.Name != "Fixture starter" {
		t.Errorf("Get() = version %d name %q, want version 2 name %q", m.Version, m.Name, "Fixture starter")
	}
	if len(m.Schema) != 1 || len(m.Formulas) != 2 || len(m.Skills) != 1 {
		t.Fatalf("Get() decoded %d schema / %d formulas / %d skills entries, want 1 / 2 / 1",
			len(m.Schema), len(m.Formulas), len(m.Skills))
	}

	view := m.Formulas[0]
	if view.Label != "All samples" {
		t.Errorf("view.Label = %q, want %q", view.Label, "All samples")
	}
	if len(view.Params) != 1 || view.Params[0].Source != "lookup" || !view.Params[0].Optional {
		t.Errorf("view params = %+v, want one optional lookup param", view.Params)
	}
	wantLookup := domain.StarterLookupConfig{Table: "samples", LabelColumn: "label", ValueColumn: "label"}
	if view.Params[0].Lookup == nil || !reflect.DeepEqual(*view.Params[0].Lookup, wantLookup) {
		t.Errorf("view lookup = %+v, want %+v", view.Params[0].Lookup, wantLookup)
	}

	action := m.Formulas[1]
	if action.Label != "" {
		t.Errorf("action.Label = %q, want empty — an unlabelled formula falls back to its name", action.Label)
	}
	if action.AttachedTo != "samples-list" {
		t.Errorf("action.AttachedTo = %q, want %q", action.AttachedTo, "samples-list")
	}

	if got := m.Skills[0].Targets; len(got) != 1 || got[0] != "samples" {
		t.Errorf("skill targets = %v, want [samples]", got)
	}

	if _, err := src.Get(t.Context(), "nope"); !errors.Is(err, domain.ErrStarterNotFound) {
		t.Errorf("Get(nope) error = %v, want ErrStarterNotFound", err)
	}
}

// TestNewSourceFromFSDecodesSeed pins the seed's decoded shape: the rows arrive as scalars the database binds
// as written — a whole YAML number is an int64, never the float64 JSON decoding hands over.
func TestNewSourceFromFSDecodesSeed(t *testing.T) {
	fsys := fstest.MapFS{
		"builtin/fixture.yaml": &fstest.MapFile{Data: []byte(fixtureManifest)},
	}
	src, err := newSourceFromFS(fsys, "builtin/*.yaml")
	if err != nil {
		t.Fatalf("newSourceFromFS() error = %v", err)
	}
	m, err := src.Get(t.Context(), "fixture")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	want := []domain.StarterSeed{{
		Table:   "samples",
		Columns: []string{"id", "label"},
		Rows:    [][]any{{int64(1), "first"}, {int64(2), "second"}},
	}}
	if !reflect.DeepEqual(m.Seed, want) {
		t.Errorf("Seed = %#v, want %#v", m.Seed, want)
	}
}

// TestNewSourceFromFSRejectsUnknownFields verifies strict decoding fails loudly on a misspelled field instead
// of silently dropping it.
func TestNewSourceFromFSRejectsUnknownFields(t *testing.T) {
	//nolint:misspell // "descriptoin" is the misspelled field under test
	fsys := fstest.MapFS{
		"builtin/typo.yaml": &fstest.MapFile{
			Data: []byte("key: typo\nversion: 1\nname: Typo\ndescriptoin: oops\nschema: [CREATE TABLE t (id INTEGER)]"),
		},
	}

	if _, err := newSourceFromFS(fsys, "builtin/*.yaml"); err == nil {
		t.Fatal("newSourceFromFS() = nil error, want strict-decoding failure for unknown field")
	}
}

// TestNewSource loads the real embedded manifests: whatever ships in builtin/ must decode and satisfy
// load-time validation.
func TestNewSource(t *testing.T) {
	src, err := NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}

	manifests, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(manifests) == 0 {
		t.Fatal("List() returned no built-in starters, want at least one")
	}

	if _, err := src.Get(t.Context(), "shopping-pantry"); err != nil {
		t.Errorf("Get(shopping-pantry) error = %v", err)
	}
}

// TestSortByCuratedOrder pins the catalog ordering: listed keys lead in the order given, unlisted ones
// follow in their existing relative order, and a key naming no manifest is simply skipped.
func TestSortByCuratedOrder(t *testing.T) {
	tests := []struct {
		name  string
		keys  []string
		order []string
		want  []string
	}{
		{
			name:  "listed keys lead in the order given",
			keys:  []string{"ai-memory", "groceries", "notes", "places"},
			order: []string{"notes", "groceries"},
			want:  []string{"notes", "groceries", "ai-memory", "places"},
		},
		{
			name:  "unlisted keys keep their relative order",
			keys:  []string{"workouts", "ai-memory", "notes", "places"},
			order: []string{"notes"},
			want:  []string{"notes", "workouts", "ai-memory", "places"},
		},
		{
			name:  "an order naming a missing manifest skips it",
			keys:  []string{"notes", "places"},
			order: []string{"gone", "places"},
			want:  []string{"places", "notes"},
		},
		{
			name:  "an empty order leaves the input untouched",
			keys:  []string{"places", "notes"},
			order: nil,
			want:  []string{"places", "notes"},
		},
		{
			name:  "every key listed",
			keys:  []string{"places", "notes"},
			order: []string{"notes", "places"},
			want:  []string{"notes", "places"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifests := make([]domain.StarterManifest, len(tt.keys))
			for i, k := range tt.keys {
				manifests[i] = domain.StarterManifest{Key: k}
			}

			sortByCuratedOrder(manifests, tt.order)

			got := make([]string, len(manifests))
			for i, m := range manifests {
				got[i] = m.Key
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("sortByCuratedOrder() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCuratedOrderNamesRealStarters guards the one failure mode the ordering has: a key in
// curatedOrder that matches no manifest is not an error, so a typo silently drops that starter into
// the unlisted tail, in the wrong place, with every other test still green.
func TestCuratedOrderNamesRealStarters(t *testing.T) {
	src, err := NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	manifests, err := src.List(t.Context())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	keys := make(map[string]bool, len(manifests))
	for _, m := range manifests {
		keys[m.Key] = true
	}
	for _, key := range curatedOrder {
		if !keys[key] {
			t.Errorf("curatedOrder names %q, which no manifest carries", key)
		}
	}
}
