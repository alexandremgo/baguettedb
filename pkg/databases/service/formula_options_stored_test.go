package service

import (
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// TestUnmarshalParams_LegacyFlatOptions covers the lazy read migration: option sets stored before
// labels existed carried bare value strings, which fold into {value, label} entries on read — and
// the next write persists the object shape, so a rewritten schema never re-reads the flat form.
func TestUnmarshalParams_LegacyFlatOptions(t *testing.T) {
	legacy := `[{"name":"kind","type":"text","source":"prompt","optional":true,"options":["film","tv"]}]`
	params, err := unmarshalParams(legacy)
	if err != nil {
		t.Fatalf("unmarshalParams(legacy) error = %v", err)
	}
	if len(params) != 1 {
		t.Fatalf("params length = %d, want 1", len(params))
	}
	want := []domain.FormulaOption{{Value: "film", Label: "film"}, {Value: "tv", Label: "tv"}}
	got := params[0].Options
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("options = %+v, want values folded to labels %+v", got, want)
	}

	stored, err := marshalParams(params)
	if err != nil {
		t.Fatalf("marshalParams() error = %v", err)
	}
	if !strings.Contains(stored, `"options":[{"value":"film","label":"film"},{"value":"tv","label":"tv"}]`) {
		t.Errorf("re-marshaled params = %s, want the normalized object entries", stored)
	}
}
