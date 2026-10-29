package appinput_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/appinput"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// TestCreateFormula_Input_DecodesEveryField checks that a creation request reaches the service input: the scalars, a
// parameter list with a lookup, options and numeric defaults, a group with its fields, the attachment and the display.
func TestCreateFormula_Input_DecodesEveryField(t *testing.T) {
	var req appinput.CreateFormula
	if err := json.Unmarshal([]byte(`{
		"name": "add-task", "label": "Add task", "description": "Adds a task",
		"sql": "INSERT INTO tasks (name) VALUES (:name)",
		"params": [
			{"name": "name", "type": "text", "default": "all"},
			{"name": "list", "type": "integer",
				"lookup": {"table": "lists", "label_column": "name", "value_column": "id"}},
			{"name": "tags", "type": "text", "multiple": true,
				"options": ["a", 2, {"value": "c", "label": "C"}], "defaults": ["a", 2]},
			{"name": "items", "type": "group", "fields": [{"name": "qty", "type": "integer"}]}
		],
		"attached_to_formula_id": "7b0c8a52-8d1e-4a35-9c5d-2f1e0c3b4a69",
		"display_order": 3,
		"display": {"mode": "table", "slots": {"add": "add-task", "bulk": ["done"]}},
		"hidden": true
	}`), &req); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	input, err := req.Input()
	if err != nil {
		t.Fatalf("Input: %v", err)
	}
	if input.Name != "add-task" || input.Label != "Add task" || input.SQL == "" || !input.Hidden {
		t.Errorf("scalars = %+v", input)
	}
	if len(input.Params) != 4 {
		t.Fatalf("params = %d, want 4", len(input.Params))
	}
	if input.Params[0].Default != "all" {
		t.Errorf("default = %q, want all", input.Params[0].Default)
	}
	if lookup := input.Params[1].Lookup; lookup == nil || lookup.Table != "lists" || lookup.ValueColumn != "id" {
		t.Errorf("lookup = %+v", lookup)
	}
	tags := input.Params[2]
	wantOptions := []domain.FormulaOption{{Value: "a", Label: "a"}, {Value: "2", Label: "2"}, {Value: "c", Label: "C"}}
	if len(tags.Options) != len(wantOptions) {
		t.Fatalf("options = %+v, want %+v", tags.Options, wantOptions)
	}
	for i, o := range wantOptions {
		if tags.Options[i] != o {
			t.Errorf("option %d = %+v, want %+v", i, tags.Options[i], o)
		}
	}
	if strings.Join(tags.Defaults, ",") != "a,2" {
		t.Errorf("defaults = %v, want [a 2]", tags.Defaults)
	}
	if fields := input.Params[3].Fields; len(fields) != 1 || fields[0].Name != "qty" {
		t.Errorf("group fields = %+v", fields)
	}
	if input.AttachedToFormulaID == nil || input.DisplayOrder == nil || *input.DisplayOrder != 3 {
		t.Errorf("attachment = %v, display order = %v", input.AttachedToFormulaID, input.DisplayOrder)
	}
	if d := input.Display; d == nil || d.Mode != "table" || d.Slots.Add != "add-task" ||
		strings.Join(d.Slots.Bulk, ",") != "done" {
		t.Errorf("display = %+v", d)
	}
}

// TestCreateFormula_Input_RejectsAnAttachmentThatIsNotAUUID checks that an attachment is a UUID or absent.
func TestCreateFormula_Input_RejectsAnAttachmentThatIsNotAUUID(t *testing.T) {
	req := appinput.CreateFormula{Name: "f", AttachedToFormulaID: new(string)}
	if _, err := req.Input(); err != nil {
		t.Errorf("an empty attachment should mean top-level, got %v", err)
	}
	*req.AttachedToFormulaID = "not-a-uuid"
	if _, err := req.Input(); err == nil || err.Error() != "invalid attached_to_formula_id" {
		t.Errorf("error = %v, want invalid attached_to_formula_id", err)
	}
}

// TestUpdateFormula_Patch_SetsWhatIsPresent checks the patch semantics: an absent field stays unset, a present one is
// set even when empty, params replace the schema, and display is tri-state (absent unchanged, null cleared, object
// replaced, anything else refused).
func TestUpdateFormula_Patch_SetsWhatIsPresent(t *testing.T) {
	patch := decodePatch(t, `{"label": "", "params": []}`)
	if patch.Name != nil || patch.Description != nil || patch.SQL != nil || patch.Hidden != nil {
		t.Errorf("absent fields should stay unset: %+v", patch)
	}
	if patch.Label == nil || *patch.Label != "" {
		t.Errorf("a present empty label should be set, got %v", patch.Label)
	}
	if patch.Params == nil || len(*patch.Params) != 0 {
		t.Errorf("present params should replace the schema, got %v", patch.Params)
	}
	if patch.Display != nil || patch.ClearDisplay {
		t.Errorf("an absent display should stay unchanged: %+v", patch)
	}

	if cleared := decodePatch(t, `{"display": null}`); !cleared.ClearDisplay || cleared.Display != nil {
		t.Errorf("a null display should clear it: %+v", cleared)
	}
	if replaced := decodePatch(t, `{"display": {"mode": "list"}}`); replaced.ClearDisplay ||
		replaced.Display == nil || replaced.Display.Mode != "list" {
		t.Errorf("an object display should replace it: %+v", replaced)
	}

	var req appinput.UpdateFormula
	if err := json.Unmarshal([]byte(`{"display": "list"}`), &req); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if _, err := req.Patch(); err == nil || err.Error() != "display must be an object or null" {
		t.Errorf("a string display should be refused, got %v", err)
	}
}

// decodePatch decodes an update request and converts it, failing the test on any error.
func decodePatch(t *testing.T, raw string) service.FormulaPatch {
	t.Helper()
	var req appinput.UpdateFormula
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	patch, err := req.Patch()
	if err != nil {
		t.Fatalf("Patch(%s): %v", raw, err)
	}
	return patch
}

// TestFormulaParam_RejectsMalformedDefaults checks that a defaults entry is a string or a number.
func TestFormulaParam_RejectsMalformedDefaults(t *testing.T) {
	var p appinput.FormulaParam
	err := json.Unmarshal([]byte(`{"name": "tags", "type": "text", "defaults": [true]}`), &p)
	if err == nil || !strings.Contains(err.Error(), "a defaults entry must be a string or a number") {
		t.Errorf("error = %v, want a defaults entry must be a string or a number", err)
	}
}

// TestFormulaDefault_RefusesANonString checks that a parameter's default decodes from a string or null, and that any
// other shape is refused with a message pointing at defaults, where a multi-select's subset goes.
func TestFormulaDefault_RefusesANonString(t *testing.T) {
	for _, raw := range []string{`{"name": "a", "default": "all"}`, `{"name": "a", "default": null}`, `{"name": "a"}`} {
		var p appinput.FormulaParam
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}

	var p appinput.FormulaParam
	err := json.Unmarshal([]byte(`{"name": "tags", "default": ["film", "tv"]}`), &p)
	want := "default must be a string - a multi-select's subset goes in defaults"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}
