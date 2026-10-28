//go:build integration

package integrationtest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// formulasPath returns the API path for formula endpoints scoped to a bdb.
func formulasPath(bdbID, suffix string) string {
	base := fmt.Sprintf("/api/databases/v1/bdbs/%s/formulas", bdbID)
	if suffix == "" {
		return base
	}
	return base + "/" + suffix
}

// createInventoryTable provisions a table the formula SQL below can reference.
func createInventoryTable(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE inventory (id INTEGER PRIMARY KEY, quantity INTEGER, supplier TEXT)"})
}

// createFormulaAPI creates a formula and returns the created response body.
func createFormulaAPI(
	t *testing.T, router http.Handler, bdbID, userID string, body map[string]any,
) map[string]any {
	t.Helper()
	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), body)
	if status != http.StatusCreated {
		t.Fatalf("create formula %q status = %d, want 201, body: %v", body["name"], status, resp)
	}
	return resp
}

// --- Public REST CRUD ---

func TestFormulas_REST_CreateAndGet(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "low-stock",
			"description": "Items below a threshold.",
			"sql":         "SELECT rowid, * FROM inventory WHERE quantity < :max",
			"params": []map[string]any{
				{"name": "max", "type": "integer", "source": "prompt"},
			},
		})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201, body: %v", status, resp)
	}
	id := resp["id"].(string)
	if _, err := uuid.Parse(id); err != nil {
		t.Errorf("created id is not a UUID: %v", id)
	}
	if resp["kind"] != "view" {
		t.Errorf("kind = %v, want view (a SELECT)", resp["kind"])
	}
	if resp["statement_verb"] != "select" {
		t.Errorf("statement_verb = %v, want select", resp["statement_verb"])
	}
	params, _ := resp["params"].([]any)
	if len(params) != 1 {
		t.Fatalf("params length = %d, want 1", len(params))
	}

	// Fetches by name.
	status, resp = testutil.DoRequest(t, router, http.MethodGet, formulasPath(bdbID, "low-stock"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get-by-name status = %d, want 200, body: %v", status, resp)
	}
	if resp["sql"] != "SELECT rowid, * FROM inventory WHERE quantity < :max" {
		t.Errorf("sql = %v, want the stored SQL", resp["sql"])
	}

	// Fetches by UUID.
	status, resp = testutil.DoRequest(t, router, http.MethodGet, formulasPath(bdbID, id),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get-by-uuid status = %d, want 200, body: %v", status, resp)
	}
	if resp["name"] != "low-stock" {
		t.Errorf("name = %v, want low-stock", resp["name"])
	}
}

// TestFormulas_REST_ParamFlagsRoundTrip pins the store→load path for the schema-shaping param
// fields: inline, options, default, when, and a lookup's allow_new must read back from a GET
// exactly as created — with when's omitted op staying omitted rather than materializing as "eq",
// and bare-string options reading back in the normalized {value, label} form.
func TestFormulas_REST_ParamFlagsRoundTrip(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	created := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "add-stock",
		"description": "Adds an inventory row.",
		"sql": "INSERT INTO inventory (quantity, supplier) SELECT COALESCE(:quantity, 0), :supplier " +
			"WHERE :kind IN ('bulk', 'unit') AND (:pallets IS NULL OR :pallets > 0) " +
			"AND (:boxes IS NULL OR :boxes >= 0)",
		"params": []map[string]any{
			{
				"name": "kind", "type": "text", "source": "prompt", "optional": true,
				"options": []string{"bulk", "unit"}, "default": "bulk", "inline": true,
			},
			{
				"name": "quantity", "type": "integer", "source": "prompt", "optional": true, "inline": true,
				"when": map[string]any{"param": "kind", "value": "unit"},
			},
			{"name": "pallets", "type": "integer", "source": "prompt", "optional": true},
			{
				"name": "boxes", "type": "integer", "source": "prompt", "optional": true,
				"when": map[string]any{"param": "pallets", "op": "gt", "value": "100"},
			},
			{
				"name": "supplier", "type": "text", "source": "lookup", "optional": true,
				"lookup": map[string]any{
					"table": "inventory", "label_column": "supplier", "value_column": "supplier",
					"allow_new": true, "scope": map[string]any{"kind": "supplier"},
					"fill": map[string]any{"pallets": "quantity"},
				},
			},
		},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodGet,
		formulasPath(bdbID, created["id"].(string)), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200, body: %v", status, resp)
	}
	params, _ := resp["params"].([]any)
	if len(params) != 5 {
		t.Fatalf("params length = %d, want 5, body: %v", len(params), resp)
	}
	kind, _ := params[0].(map[string]any)
	if kind["inline"] != true {
		t.Errorf("kind inline = %v, want true", kind["inline"])
	}
	options, _ := kind["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("kind options = %v, want 2 entries", kind["options"])
	}
	for i, want := range []string{"bulk", "unit"} {
		if opt, _ := options[i].(map[string]any); opt["value"] != want || opt["label"] != want {
			t.Errorf("kind options[%d] = %v, want value = label = %q", i, options[i], want)
		}
	}
	if kind["default"] != "bulk" {
		t.Errorf("kind default = %v, want bulk", kind["default"])
	}
	quantity, _ := params[1].(map[string]any)
	when, _ := quantity["when"].(map[string]any)
	if when["param"] != "kind" || when["value"] != "unit" {
		t.Errorf("quantity when = %v, want kind = unit", quantity["when"])
	}
	if _, present := when["op"]; present {
		t.Errorf("quantity when op = %v, want the omitted default left omitted", when["op"])
	}
	boxes, _ := params[3].(map[string]any)
	if when, _ := boxes["when"].(map[string]any); when["param"] != "pallets" ||
		when["op"] != "gt" || when["value"] != "100" {
		t.Errorf("boxes when = %v, want pallets gt 100", boxes["when"])
	}
	supplier, _ := params[4].(map[string]any)
	lookup, _ := supplier["lookup"].(map[string]any)
	if lookup["allow_new"] != true {
		t.Errorf("supplier lookup = %v, want allow_new kept true", supplier["lookup"])
	}
	if scope, _ := lookup["scope"].(map[string]any); scope["kind"] != "supplier" {
		t.Errorf("supplier lookup scope = %v, want the kind narrowing kept", lookup["scope"])
	}
	if fill, _ := lookup["fill"].(map[string]any); fill["pallets"] != "quantity" {
		t.Errorf("supplier lookup fill = %v, want the pallets fill kept", lookup["fill"])
	}
}

// TestFormulas_REST_DefaultReadsBackAsAuthored pins the two default fields over REST: a scalar default
// and the "all" literal read back as the strings they are, a multi-select's defaults subset reads back as
// an array (numbers as numbers), and a mistyped default is rejected at the door.
func TestFormulas_REST_DefaultReadsBackAsAuthored(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedMembersTally(t, router, bdbID, userID)

	created := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "tag-some",
		"description": "Tags members.",
		"sql": "INSERT INTO tally (member_id, tag) SELECT je.value, :tag FROM json_each(:picked) je " +
			"WHERE json_array_length(:habits) >= 0 AND json_array_length(:everyone) >= 0",
		"params": []map[string]any{
			{"name": "tag", "type": "text", "source": "prompt", "optional": true, "default": "usual"},
			{"name": "picked", "type": "integer", "source": "lookup", "multiple": true, "defaults": []any{3, 1},
				"lookup": map[string]any{"table": "members", "label_column": "name", "value_column": "id"}},
			{"name": "habits", "type": "text", "source": "prompt", "optional": true, "multiple": true,
				"options": []string{"staple", "one-off"}, "defaults": []any{"staple"}},
			{"name": "everyone", "type": "integer", "source": "lookup", "multiple": true, "default": "all",
				"lookup": map[string]any{"table": "members", "label_column": "name", "value_column": "id"}},
		},
	})
	status, resp := testutil.DoRequest(t, router, http.MethodGet,
		formulasPath(bdbID, created["id"].(string)), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200", status)
	}
	params, _ := resp["params"].([]any)
	byName := map[string]map[string]any{}
	for _, raw := range params {
		p, _ := raw.(map[string]any)
		byName[p["name"].(string)] = p
	}
	if byName["tag"]["default"] != "usual" {
		t.Errorf("scalar default = %v, want the string usual", byName["tag"]["default"])
	}
	if got, _ := byName["picked"]["defaults"].([]any); len(got) != 2 || got[0] != 3.0 || got[1] != 1.0 {
		t.Errorf("lookup multi defaults = %v, want the array [3, 1] with numbers", byName["picked"]["defaults"])
	}
	if got, _ := byName["habits"]["defaults"].([]any); len(got) != 1 || got[0] != "staple" {
		t.Errorf("options multi defaults = %v, want the array [staple]", byName["habits"]["defaults"])
	}
	if byName["everyone"]["default"] != "all" {
		t.Errorf("all default = %v, want the string all", byName["everyone"]["default"])
	}

	status, _ = testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""), userToken(t, userID),
		map[string]any{
			"name": "bad-default", "description": "x", "sql": "SELECT :n AS n",
			"params": []map[string]any{{"name": "n", "type": "integer", "source": "prompt", "optional": true,
				"default": map[string]any{"value": 3}}},
		})
	if status != http.StatusBadRequest {
		t.Errorf("create with an object default: status = %d, want 400", status)
	}
}

// TestFormulas_REST_LookupFillRejectsAMissingColumn pins the save-time guard on a fill: a column the
// lookup table does not have is refused like a missing label or value column, so a picker can never
// try to copy from a column that is not there.
func TestFormulas_REST_LookupFillRejectsAMissingColumn(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""), userToken(t, userID),
		map[string]any{
			"name":        "add-stock",
			"description": "Adds an inventory row.",
			"sql":         "INSERT INTO inventory (quantity, supplier) VALUES (:quantity, :supplier)",
			"params": []map[string]any{
				{"name": "quantity", "type": "integer", "source": "prompt", "optional": true},
				{
					"name": "supplier", "type": "text", "source": "lookup",
					"lookup": map[string]any{
						"table": "inventory", "label_column": "supplier", "value_column": "supplier",
						"allow_new": true, "fill": map[string]any{"quantity": "no_such_column"},
					},
				},
			},
		})
	if status != http.StatusBadRequest {
		t.Fatalf("create with a fill on a missing column: status = %d, want 400", status)
	}
}

// TestFormulas_REST_LabeledOptionsNormalize pins the mixed-list input contract: an options entry is
// a bare string, a bare number, or a {value, label} object, and reads always return the normalized
// object form — label folding to the value when the input carried none, a numeric value to its
// literal string form.
func TestFormulas_REST_LabeledOptionsNormalize(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	created := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "monthly-stock",
		"description": "Stock received in a month.",
		"sql": "SELECT rowid, * FROM inventory WHERE (:month IS NULL OR :month > 0) " +
			"AND (:pay IS NULL OR :pay != '')",
		"params": []map[string]any{
			{
				"name": "month", "type": "integer", "source": "prompt", "optional": true,
				"options": []any{map[string]any{"value": 9, "label": "September"}, 10},
			},
			{
				"name": "pay", "type": "text", "source": "prompt", "optional": true,
				"options": []any{"cash", map[string]any{"value": "card", "label": "Credit card"}},
			},
		},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodGet,
		formulasPath(bdbID, created["id"].(string)), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200, body: %v", status, resp)
	}
	params, _ := resp["params"].([]any)
	if len(params) != 2 {
		t.Fatalf("params length = %d, want 2, body: %v", len(params), resp)
	}
	month, _ := params[0].(map[string]any)
	for i, want := range []map[string]any{
		{"value": "9", "label": "September"},
		{"value": "10", "label": "10"},
	} {
		options, _ := month["options"].([]any)
		if opt, _ := options[i].(map[string]any); opt["value"] != want["value"] || opt["label"] != want["label"] {
			t.Errorf("month options[%d] = %v, want %v", i, options[i], want)
		}
	}
	pay, _ := params[1].(map[string]any)
	for i, want := range []map[string]any{
		{"value": "cash", "label": "cash"},
		{"value": "card", "label": "Credit card"},
	} {
		options, _ := pay["options"].([]any)
		if opt, _ := options[i].(map[string]any); opt["value"] != want["value"] || opt["label"] != want["label"] {
			t.Errorf("pay options[%d] = %v, want %v", i, options[i], want)
		}
	}
}

// TestFormulas_REST_OptionsRejected pins the create-time option guards on the new typed sets: a
// value that does not read as the param's type, two labels differing only by case, and an
// option-incapable param type are each a 400, not a stored dud.
func TestFormulas_REST_OptionsRejected(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	for _, tt := range []struct {
		label   string
		typ     string
		options []any
	}{
		{"non-integer value on an integer param", "integer", []any{"soon"}},
		{"labels differing only by case", "integer", []any{
			map[string]any{"value": 1, "label": "One"},
			map[string]any{"value": 2, "label": "one"},
		}},
		{"options on a boolean param", "boolean", []any{"true", "false"}},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
			userToken(t, userID), map[string]any{
				"name":        "monthly-stock",
				"description": "Stock received in a month.",
				"sql":         "SELECT rowid, * FROM inventory WHERE :x IS NULL",
				"params": []map[string]any{{
					"name": "x", "type": tt.typ, "source": "prompt", "optional": true, "options": tt.options,
				}},
			})
		if status != http.StatusBadRequest {
			t.Errorf("%s: create status = %d, want 400, body: %v", tt.label, status, resp)
		}
	}
}

// TestFormulas_REST_WhenRejected pins the create-time guard: a condition gating on an undeclared
// sibling is a 400, not a stored dud.
func TestFormulas_REST_WhenRejected(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), map[string]any{
			"name":        "add-stock",
			"description": "Adds an inventory row.",
			"sql":         "INSERT INTO inventory (quantity) VALUES (COALESCE(:quantity, 0))",
			"params": []map[string]any{
				{
					"name": "quantity", "type": "integer", "source": "prompt", "optional": true,
					"when": map[string]any{"param": "undeclared-controller", "value": "unit"},
				},
			},
		})
	if status != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400, body: %v", status, resp)
	}
}

// TestFormulas_REST_LabelRoundTrip pins the human label: it survives create and GET, an update
// replaces it, and a formula created without one comes back with no label at all rather than an
// echo of the name — the app supplies that fallback.
func TestFormulas_REST_LabelRoundTrip(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	created := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "low-stock",
		"label":       "Running low",
		"description": "Inventory rows below the threshold.",
		"sql":         "SELECT rowid AS rowid, quantity FROM inventory WHERE quantity < 5",
	})
	if created["label"] != "Running low" {
		t.Errorf("create label = %v, want Running low", created["label"])
	}

	formulaID := created["id"].(string)
	status, resp := testutil.DoRequest(t, router, http.MethodGet,
		formulasPath(bdbID, formulaID), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200, body: %v", status, resp)
	}
	if resp["label"] != "Running low" {
		t.Errorf("get label = %v, want Running low", resp["label"])
	}
	if resp["name"] != "low-stock" {
		t.Errorf("get name = %v, want low-stock — the label must not replace the reference", resp["name"])
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPut,
		formulasPath(bdbID, formulaID), userToken(t, userID),
		map[string]any{"label": "Almost out"})
	if status != http.StatusOK {
		t.Fatalf("update status = %d, want 200, body: %v", status, resp)
	}
	if resp["label"] != "Almost out" {
		t.Errorf("updated label = %v, want Almost out", resp["label"])
	}

	unlabelled := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "all-stock",
		"description": "Every inventory row.",
		"sql":         "SELECT rowid AS rowid, quantity FROM inventory",
	})
	if _, present := unlabelled["label"]; present {
		t.Errorf("unlabelled formula carries label = %v, want the key omitted", unlabelled["label"])
	}
}

// TestFormulas_REST_LabelRejected pins the label guards at the handler boundary: too long, and
// carrying an invisible character that would make the rendered label differ from the stored one.
func TestFormulas_REST_LabelRejected(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	cases := []struct {
		name  string
		label string
	}{
		{"too long", strings.Repeat("a", 61)},
		{"zero-width space", "Running\u200blow"},
		{"newline", "Running\nlow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
				userToken(t, userID),
				map[string]any{
					"name":        "low-stock",
					"label":       tc.label,
					"description": "Inventory rows below the threshold.",
					"sql":         "SELECT rowid AS rowid, quantity FROM inventory WHERE quantity < 5",
				})
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400, body: %v", status, resp)
			}
		})
	}
}

// TestFormulas_REST_InlineOnGroupRejected pins the authoring guard: the inline flag places a param
// in the quick-add form, and a repeated group only fits the dialog, so declaring both is rejected.
func TestFormulas_REST_InlineOnGroupRejected(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "add-lines",
			"description": "Adds inventory rows.",
			"sql":         "INSERT INTO inventory (quantity) SELECT je.value FROM json_each(:lines) je",
			"params": []map[string]any{
				{
					"name": "lines", "type": "group", "source": "prompt", "inline": true,
					"fields": []map[string]any{
						{"name": "quantity", "type": "integer", "source": "prompt"},
					},
				},
			},
		})
	if status != http.StatusBadRequest {
		t.Errorf("inline group: status = %d, want 400, body: %v", status, resp)
	}
}

// TestFormulas_REST_StatementVerbDerived pins the server-side statement verb: it is read from the
// prepared statement's authorizer actions, so comment- and CTE-led SQL still reports its real verb
// (the shape a first-token sniffer would misread), and an update that changes the SQL re-derives it.
func TestFormulas_REST_StatementVerbDerived(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "purge-depleted",
			"description": "Delete depleted items.",
			"sql": "-- purge depleted stock\nWITH depleted AS (SELECT id FROM inventory WHERE quantity <= 0) " +
				"DELETE FROM inventory WHERE id IN (SELECT id FROM depleted)",
		})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201, body: %v", status, resp)
	}
	if resp["kind"] != "action" {
		t.Errorf("kind = %v, want action", resp["kind"])
	}
	if resp["statement_verb"] != "delete" {
		t.Errorf("statement_verb = %v, want delete (comment- and CTE-led DELETE)", resp["statement_verb"])
	}

	// Changing the SQL re-derives kind and verb together.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "purge-depleted"),
		userToken(t, userID),
		map[string]any{"sql": "UPDATE inventory SET quantity = 0"})
	if status != http.StatusOK {
		t.Fatalf("update status = %d, want 200, body: %v", status, resp)
	}
	if resp["statement_verb"] != "update" {
		t.Errorf("statement_verb after SQL change = %v, want update", resp["statement_verb"])
	}
}

// TestFormulas_REST_KindFlipOnUpdate pins that a SQL change re-derives the kind: an unattached
// view may freely become an action (and its verb follows).
func TestFormulas_REST_KindFlipOnUpdate(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "stock", "description": "x", "sql": "SELECT id FROM inventory",
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "stock"),
		userToken(t, userID), map[string]any{"sql": "DELETE FROM inventory"})
	if status != http.StatusOK {
		t.Fatalf("update status = %d, want 200, body: %v", status, resp)
	}
	if resp["kind"] != "action" || resp["statement_verb"] != "delete" {
		t.Errorf("kind/verb = %v/%v, want action/delete", resp["kind"], resp["statement_verb"])
	}
}

// TestFormulas_REST_AttachConflicts pins the attachment invariant at create time: a formula can
// only attach to a view, and an attached view must qualify as a row detail (here the param-less
// view has no :rowid to bind).
func TestFormulas_REST_AttachConflicts(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	action := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "clear-stock", "description": "x", "sql": "UPDATE inventory SET quantity = 0",
	})
	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "stock", "description": "x", "sql": "SELECT rowid AS rowid, id FROM inventory",
	})

	singleRowParams := []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}}
	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), map[string]any{
			"name": "attached-to-action", "description": "x",
			"sql":                    "UPDATE inventory SET quantity = 1 WHERE rowid = :rowid",
			"params":                 singleRowParams,
			"attached_to_formula_id": action["id"],
		})
	if status != http.StatusBadRequest {
		t.Errorf("attach to an action: status = %d, want 400, body: %v", status, resp)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), map[string]any{
			"name": "view-attached-to-view", "description": "x",
			"sql":                    "SELECT id FROM inventory",
			"attached_to_formula_id": view["id"],
		})
	if status != http.StatusBadRequest {
		t.Errorf("attach a view: status = %d, want 400, body: %v", status, resp)
	}
}

// TestFormulas_REST_KindLockedByAttachment pins the attachment invariant at update time: a parent
// view with attached actions must stay a view, and an attached action must stay an action.
func TestFormulas_REST_KindLockedByAttachment(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "stock", "description": "x", "sql": "SELECT rowid AS rowid, id FROM inventory",
	})
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "zero-item", "description": "x",
		"sql":                    "UPDATE inventory SET quantity = 0 WHERE rowid = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": view["id"],
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "stock"),
		userToken(t, userID), map[string]any{"sql": "DELETE FROM inventory"})
	if status != http.StatusBadRequest {
		t.Errorf("flip parent view to DML: status = %d, want 400, body: %v", status, resp)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "zero-item"),
		userToken(t, userID), map[string]any{"sql": "SELECT id FROM inventory", "params": []map[string]any{}})
	if status != http.StatusBadRequest {
		t.Errorf("flip attached action to SELECT: status = %d, want 400, body: %v", status, resp)
	}
}

// TestFormulas_REST_BulkPlaceholderValidatedAtCreate pins that a bulk action's SQL must hold
// exactly one literal :rowids token — a different sigil or a second occurrence inside a string
// literal would otherwise create fine and fail expansion on every run.
func TestFormulas_REST_BulkPlaceholderValidatedAtCreate(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	for name, sql := range map[string]string{
		"at-sigil":                 "DELETE FROM inventory WHERE rowid IN (@rowids)",
		"second-in-string-literal": "DELETE FROM inventory WHERE rowid IN (:rowids) AND ':rowids' <> ''",
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
			userToken(t, userID), map[string]any{
				"name": "bulk-" + name, "description": "x", "sql": sql,
				"params": []map[string]any{{"name": "rowids", "type": "integer", "source": "selection-bulk"}},
			})
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body: %v", name, status, resp)
		}
	}
}

func TestFormulas_REST_NameMustNotBeUUIDShaped(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), map[string]any{
			"name": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "description": "x",
			"sql": "SELECT id FROM inventory",
		})
	if status != http.StatusBadRequest {
		t.Errorf("UUID-shaped name: status = %d, want 400, body: %v", status, resp)
	}
}

func TestFormulas_REST_PaginationParamsDeclaredTogether(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), map[string]any{
			"name": "cursor-only", "description": "x",
			"sql":    "SELECT rowid AS rowid, id FROM inventory WHERE rowid > :cursor ORDER BY rowid",
			"params": []map[string]any{{"name": "cursor", "type": "integer", "source": "pagination"}},
		})
	if status != http.StatusBadRequest {
		t.Errorf("cursor without limit: status = %d, want 400, body: %v", status, resp)
	}
}

func TestFormulas_REST_ListOrderedByDisplayOrder(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	// Created in this order; each appends, so display_order is creation order.
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
			userToken(t, userID),
			map[string]any{
				"name":        name,
				"description": "desc " + name,
				"sql":         "SELECT rowid, * FROM inventory",
			})
	}

	status, list := testutil.DoRequestList(t, router, http.MethodGet, formulasPath(bdbID, ""),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("list status = %d, want 200", status)
	}
	if len(list) != 3 {
		t.Fatalf("list length = %d, want 3", len(list))
	}
	for i, want := range []string{"alpha", "bravo", "charlie"} {
		if list[i]["name"] != want {
			t.Errorf("list[%d] name = %v, want %v", i, list[i]["name"], want)
		}
		if got := list[i]["display_order"].(float64); int(got) != i {
			t.Errorf("list[%d] display_order = %v, want %d", i, got, i)
		}
	}
}

func TestFormulas_REST_UpdatePartial(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "to-edit",
			"description": "Original description.",
			"sql":         "SELECT rowid, * FROM inventory",
		})

	newDesc := "Updated description."
	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "to-edit"),
		userToken(t, userID),
		map[string]any{"description": newDesc})
	if status != http.StatusOK {
		t.Fatalf("update status = %d, want 200, body: %v", status, resp)
	}
	if resp["description"] != newDesc {
		t.Errorf("description = %v, want %v", resp["description"], newDesc)
	}
	if resp["sql"] != "SELECT rowid, * FROM inventory" {
		t.Errorf("sql = %v, want unchanged", resp["sql"])
	}
}

func TestFormulas_REST_Delete(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "to-delete", "description": "Bye.", "sql": "SELECT rowid, * FROM inventory"})

	status, _ := testutil.DoRequest(t, router, http.MethodDelete, formulasPath(bdbID, "to-delete"),
		userToken(t, userID), nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", status)
	}

	status, _ = testutil.DoRequest(t, router, http.MethodGet, formulasPath(bdbID, "to-delete"),
		userToken(t, userID), nil)
	if status != http.StatusNotFound {
		t.Errorf("get-after-delete status = %d, want 404", status)
	}
}

// --- Attached actions + FK cascade ---

func TestFormulas_REST_AttachedActionCascadesOnDelete(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	_, viewResp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "stock-list", "description": "All stock.", "sql": "SELECT rowid, * FROM inventory"})
	viewID := viewResp["id"].(string)

	status, actionResp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":                   "delete-row",
			"description":            "Delete selected rows.",
			"sql":                    "DELETE FROM inventory WHERE rowid = :rowid",
			"attached_to_formula_id": viewID,
			"params": []map[string]any{
				{"name": "rowid", "type": "integer", "source": "selection-single"},
			},
		})
	if status != http.StatusCreated {
		t.Fatalf("create attached action status = %d, want 201, body: %v", status, actionResp)
	}
	if actionResp["attached_to_formula_id"] != viewID {
		t.Errorf("attached_to_formula_id = %v, want %v", actionResp["attached_to_formula_id"], viewID)
	}

	// Deleting the parent view cascades to its attached action.
	testutil.DoRequest(t, router, http.MethodDelete, formulasPath(bdbID, "stock-list"),
		userToken(t, userID), nil)

	status, _ = testutil.DoRequest(t, router, http.MethodGet, formulasPath(bdbID, "delete-row"),
		userToken(t, userID), nil)
	if status != http.StatusNotFound {
		t.Errorf("attached action after parent delete: status = %d, want 404 (cascade)", status)
	}
}

// --- Error mapping ---

func TestFormulas_REST_DuplicateNameReturns409(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	body := map[string]any{"name": "dup", "description": "first", "sql": "SELECT rowid, * FROM inventory"}
	testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""), userToken(t, userID), body)

	status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), body)
	if status != http.StatusConflict {
		t.Errorf("duplicate-name status = %d, want 409", status)
	}
}

func TestFormulas_REST_InvalidNameReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "Bad Name", "description": "x", "sql": "SELECT 1"})
	if status != http.StatusBadRequest {
		t.Errorf("invalid-name status = %d, want 400", status)
	}
}

func TestFormulas_REST_AttachTargetNotFoundReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":                   "orphan",
			"description":            "x",
			"sql":                    "SELECT 1",
			"attached_to_formula_id": uuid.New().String(),
		})
	if status != http.StatusBadRequest {
		t.Errorf("missing-attach-target status = %d, want 400", status)
	}
}

// --- Permissions ---

func TestFormulas_REST_ViewerCanRead(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	ownerRouter := setupRouterWithRole(t, authz.BdbRoleOwner)
	createInventoryTable(t, ownerRouter, bdbID, userID)
	testutil.DoRequest(t, ownerRouter, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "viewer-read", "description": "x", "sql": "SELECT rowid, * FROM inventory"})

	viewerRouter := setupRouterWithRole(t, authz.BdbRoleViewer)
	status, _ := testutil.DoRequestList(t, viewerRouter, http.MethodGet, formulasPath(bdbID, ""),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Errorf("viewer list status = %d, want 200", status)
	}
}

func TestFormulas_REST_ViewerCannotWrite(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithRole(t, authz.BdbRoleViewer)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{"name": "cant-create", "description": "x", "sql": "SELECT 1"})
	if status != http.StatusForbidden {
		t.Errorf("viewer-create status = %d, want 403", status)
	}
}

func TestFormulas_REST_Unauthenticated(t *testing.T) {
	router := setupRouter(t)
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequestList(t, router, http.MethodGet, formulasPath(bdbID, ""), "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", status)
	}
}

// --- Kind derivation + param validation (enforced by domain.DeriveFormulaKind / ParamSchema.Validate) ---

func TestFormulas_REST_DDLRejectedReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "make-table",
			"description": "x",
			"sql":         "CREATE TABLE foo (id INTEGER PRIMARY KEY)",
		})
	if status != http.StatusBadRequest {
		t.Errorf("DDL formula status = %d, want 400 (kind undeterminable)", status)
	}
}

func TestFormulas_REST_MultiSelectRejectedReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "two-selects",
			"description": "x",
			"sql":         "SELECT 1; SELECT 2",
		})
	if status != http.StatusBadRequest {
		t.Errorf("multi-select formula status = %d, want 400 (a view is a single SELECT)", status)
	}
}

// TestFormulas_REST_MultiStatementAction pins the multi-statement action shape: several DML
// statements form one action, the verb reduces across statements, and the param cross-check spans
// them (:target here is referenced only by the second statement).
func TestFormulas_REST_MultiStatementAction(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	resp := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "restock-and-reset",
		"description": "Adds a restock row, then resets a depleted item.",
		"sql": "INSERT INTO inventory (quantity) VALUES (:qty); " +
			"UPDATE inventory SET quantity = 0 WHERE id = :target",
		"params": []map[string]any{
			{"name": "qty", "type": "integer", "source": "prompt"},
			{"name": "target", "type": "integer", "source": "prompt"},
		},
	})
	if resp["kind"] != "action" {
		t.Errorf("kind = %v, want action", resp["kind"])
	}
	if resp["statement_verb"] != "insert" {
		t.Errorf("statement_verb = %v, want insert (insert > update across statements)", resp["statement_verb"])
	}
}

// TestFormulas_REST_MixedStatementsRejectedReturns400 pins that an action stays all-DML: a read
// or a DDL statement anywhere in the body is rejected.
func TestFormulas_REST_MixedStatementsRejectedReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	for name, sql := range map[string]string{
		"dml then select": "UPDATE inventory SET quantity = 0; SELECT rowid, * FROM inventory",
		"dml then ddl":    "UPDATE inventory SET quantity = 0; CREATE TABLE audit (id INTEGER PRIMARY KEY)",
	} {
		status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
			userToken(t, userID),
			map[string]any{"name": "mixed", "description": "x", "sql": sql})
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (kind undeterminable)", name, status)
		}
	}
}

// TestFormulas_REST_UpdateToMultiStatement pins that a SQL change may grow an action to several
// statements: kind and verb re-derive across the new statement list.
func TestFormulas_REST_UpdateToMultiStatement(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "restock", "description": "x", "sql": "INSERT INTO inventory (quantity) VALUES (1)",
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "restock"),
		userToken(t, userID),
		map[string]any{"sql": "UPDATE inventory SET quantity = 1; DELETE FROM inventory WHERE quantity <= 0"})
	if status != http.StatusOK {
		t.Fatalf("update status = %d, want 200, body: %v", status, resp)
	}
	if resp["kind"] != "action" || resp["statement_verb"] != "delete" {
		t.Errorf("kind/verb = %v/%v, want action/delete (delete > update)", resp["kind"], resp["statement_verb"])
	}
}

func TestFormulas_REST_InvalidParamReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// "rowids" is reserved for bulk selection; pairing it with the prompt source is invalid.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "bad-params",
			"description": "x",
			"sql":         "SELECT 1",
			"params": []map[string]any{
				{"name": "rowids", "type": "integer", "source": "prompt"},
			},
		})
	if status != http.StatusBadRequest {
		t.Errorf("invalid-param formula status = %d, want 400", status)
	}
}

func TestFormulas_REST_ParamSchemaDriftReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	// The SQL uses :max but the schema declares :min — a schema/SQL mismatch the
	// create-time cross-check must reject.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "drifted",
			"description": "x",
			"sql":         "SELECT rowid, * FROM inventory WHERE quantity < :max",
			"params":      []map[string]any{{"name": "min", "type": "integer", "source": "prompt"}},
		})
	if status != http.StatusBadRequest {
		t.Errorf("param-drift status = %d, want 400", status)
	}
}

func TestFormulas_REST_AnonymousPlaceholderReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	createInventoryTable(t, router, bdbID, userID)

	// A bare "?" can't map to a named schema entry, so authored SQL must use named params.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "anon-placeholder",
			"description": "x",
			"sql":         "SELECT rowid, * FROM inventory WHERE quantity < ?",
		})
	if status != http.StatusBadRequest {
		t.Errorf("anonymous-placeholder status = %d, want 400", status)
	}
}

func TestFormulas_REST_UnknownTableReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// The SQL is a well-formed SELECT — the failure is the missing table, so the error
	// must name it, not claim the statement is the wrong kind.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "ghost-view",
			"description": "x",
			"sql":         "SELECT rowid, * FROM ghost_table",
		})
	if status != http.StatusBadRequest {
		t.Fatalf("unknown-table status = %d, want 400, body: %v", status, resp)
	}
	msg, _ := resp["error"].(string)
	if !strings.Contains(msg, "ghost_table") {
		t.Errorf("error = %q, want it to name the missing table", msg)
	}
}

// TestFormulas_REST_LookupParam pins the lookup param vertical: the config round-trips through
// create, the referenced table and columns must exist in the live schema, and a run binds the
// picked value like any scalar.
func TestFormulas_REST_LookupParam(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE exercises (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]any{"sql": "INSERT INTO exercises (id, name) VALUES (1,'Bench Press'),(2,'Squat')"})

	resp := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "exercise-by-id", "description": "One exercise, picked from a dropdown.",
		"sql": "SELECT name FROM exercises WHERE id = :exercise",
		"params": []map[string]any{{
			"name": "exercise", "type": "integer", "source": "lookup",
			"lookup": map[string]any{"table": "exercises", "label_column": "name", "value_column": "id"},
		}},
	})
	params, _ := resp["params"].([]any)
	first, _ := params[0].(map[string]any)
	echoed, _ := first["lookup"].(map[string]any)
	if echoed["table"] != "exercises" || echoed["value_column"] != "id" {
		t.Errorf("lookup config not echoed back, got %v", first["lookup"])
	}

	status, runResp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "exercise-by-id"),
		userToken(t, userID), map[string]any{"params": map[string]any{"exercise": 2}})
	if status != http.StatusOK {
		t.Fatalf("lookup run status = %d, want 200, body: %v", status, runResp)
	}
	if rows, _ := runResp["rows"].([]any); len(rows) != 1 {
		t.Errorf("rows = %d, want 1 (id = 2)", len(rows))
	}

	// Each case claims its own formula name: sharing one would make a wrongly-accepted create surface
	// as "name already taken" on the next case, reporting a collision instead of the real defect.
	for name, badLookup := range map[string]map[string]any{
		"unknown table":  {"table": "nope", "label_column": "name", "value_column": "id"},
		"unknown column": {"table": "exercises", "label_column": "nope", "value_column": "id"},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
			userToken(t, userID), map[string]any{
				"name": "bad-lookup-" + strings.ReplaceAll(name, " ", "-"), "description": "x",
				"sql": "SELECT name FROM exercises WHERE id = :exercise",
				"params": []map[string]any{{
					"name": "exercise", "type": "integer", "source": "lookup", "lookup": badLookup,
				}},
			})
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body: %v", name, status, resp)
		}
	}
}

// TestFormulas_REST_LookupOverView pins that a lookup may point at a view, not only a table: save-time
// verification reads the view's real column list — a column it does not expose is still rejected — and a
// value the view offers binds on a run like any other scalar. A self-maintaining options view is how a
// free-text column gets a dropdown without a second table to curate.
func TestFormulas_REST_LookupOverView(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE lines (id INTEGER PRIMARY KEY, store TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]any{"sql": "CREATE VIEW store_options AS SELECT DISTINCT store FROM lines WHERE store IS NOT NULL"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, userID),
		map[string]any{"sql": "INSERT INTO lines (store) " +
			"VALUES ('Green Market'),('Green Market'),('Corner Shop'),(NULL)"})

	byStore := func(name string, lookup map[string]any) map[string]any {
		return map[string]any{
			"name": name, "description": "Shopping lines, narrowed to one shop.",
			"sql": "SELECT id FROM lines WHERE :store IS NULL OR store = :store",
			"params": []map[string]any{{
				"name": "store", "type": "text", "source": "lookup", "optional": true, "lookup": lookup,
			}},
		}
	}
	createFormulaAPI(t, router, bdbID, userID, byStore("lines-by-store",
		map[string]any{"table": "store_options", "label_column": "store", "value_column": "store"}))

	// A column the view does not expose is rejected — so the create above passed because the view's
	// real column list was read, not because verification waves through anything that isn't a table.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""), userToken(t, userID),
		byStore("bad-view-lookup",
			map[string]any{"table": "store_options", "label_column": "nope", "value_column": "store"}))
	if status != http.StatusBadRequest {
		t.Fatalf("unknown view column: status = %d, want 400, body: %v", status, resp)
	}

	// A value the view offers binds like any other scalar and narrows the run.
	status, runResp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "lines-by-store"),
		userToken(t, userID), map[string]any{"params": map[string]any{"store": "Green Market"}})
	if status != http.StatusOK {
		t.Fatalf("run status = %d, want 200, body: %v", status, runResp)
	}
	if rows, _ := runResp["rows"].([]any); len(rows) != 2 {
		t.Errorf("rows = %v, want the 2 lines at that store", runResp["rows"])
	}
}

// TestFormulas_REST_DateParam pins the date param vertical: a well-formed day binds as the TEXT SQLite
// orders and compares, and a value that is not a real day is refused at the boundary.
func TestFormulas_REST_DateParam(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE batches (id INTEGER PRIMARY KEY, expires_at TEXT)"})

	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "add-batch", "description": "Records a batch with an expiry date.",
		"sql":    "INSERT INTO batches (expires_at) VALUES (:expires_at)",
		"params": []map[string]any{{"name": "expires_at", "type": "date", "source": "prompt"}},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-batch"),
		userToken(t, userID), map[string]any{"params": map[string]any{"expires_at": "2026-08-12"}})
	if status != http.StatusOK {
		t.Fatalf("run status = %d, want 200, body: %v", status, resp)
	}

	// Well-shaped but not a day that exists, plus the shapes a text param would have swallowed.
	for _, bad := range []any{"2026-02-29", "12/08/2026", "2026-8-12", "tomorrow", 20260812} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-batch"),
			userToken(t, userID), map[string]any{"params": map[string]any{"expires_at": bad}})
		if status != http.StatusBadRequest {
			t.Errorf("run with %v: status = %d, want 400, body: %v", bad, status, resp)
		}
	}

	// Only the good run reached the table: every rejection stopped before the INSERT.
	_, stored := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, userID),
		map[string]any{"sql": "SELECT expires_at FROM batches"})
	rows, _ := stored["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("stored rows = %d, want only the valid date, body: %v", len(rows), stored)
	}
	if got := extractRowValues(t, rows[0])[0]; got != "2026-08-12" {
		t.Errorf("stored date = %v, want 2026-08-12", got)
	}
}

// TestFormulas_REST_DateTimeParam pins the datetime param vertical, whose contract is normalization,
// not just admission: the browser's picker emits "2026-08-09T14:30" while SQLite's datetime() writes
// "2026-08-09 14:30:00", and a column mixing the shapes mis-orders as plain text ("T" sorts after " ").
// Every accepted form must therefore land in the column as the one canonical shape.
func TestFormulas_REST_DateTimeParam(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE sessions (id INTEGER PRIMARY KEY, started_at TEXT)"})

	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "add-session", "description": "Records a session with its start moment.",
		"sql":    "INSERT INTO sessions (started_at) VALUES (:started_at)",
		"params": []map[string]any{{"name": "started_at", "type": "datetime", "source": "prompt"}},
	})

	// The picker's "T" form and the canonical space form, in reverse chronological order of insertion:
	// only normalization makes the ORDER BY below put them back in time order.
	for _, good := range []string{"2026-08-09T14:30", "2026-08-09 09:00:00"} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-session"),
			userToken(t, userID), map[string]any{"params": map[string]any{"started_at": good}})
		if status != http.StatusOK {
			t.Fatalf("run with %q: status = %d, want 200, body: %v", good, status, resp)
		}
	}

	for _, bad := range []any{"2026-08-09", "14:30", "2026-02-29T10:00", "2026-08-09T14:30:61", "yesterday 14:30"} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-session"),
			userToken(t, userID), map[string]any{"params": map[string]any{"started_at": bad}})
		if status != http.StatusBadRequest {
			t.Errorf("run with %v: status = %d, want 400, body: %v", bad, status, resp)
		}
	}

	_, stored := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, userID),
		map[string]any{"sql": "SELECT started_at FROM sessions ORDER BY started_at"})
	rows, _ := stored["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("stored rows = %d, want the two valid datetimes, body: %v", len(rows), stored)
	}
	if got := extractRowValues(t, rows[0])[0]; got != "2026-08-09 09:00:00" {
		t.Errorf("first by text order = %v, want the morning session — the T form was stored raw", got)
	}
	if got := extractRowValues(t, rows[1])[0]; got != "2026-08-09 14:30:00" {
		t.Errorf("stored picker form = %v, want the canonical 2026-08-09 14:30:00", got)
	}

	// A declared default in the picker's "T" form binds canonicalized too — a default bound verbatim
	// would plant the mixed-shape column no caller could have produced through the checked path.
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "add-opening-session", "description": "Records a session at the default opening slot.",
		"sql": "INSERT INTO sessions (started_at) VALUES (:started_at)",
		"params": []map[string]any{{
			"name": "started_at", "type": "datetime", "source": "prompt",
			"optional": true, "default": "2026-08-10T08:00",
		}},
	})
	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-opening-session"),
		userToken(t, userID), map[string]any{"params": map[string]any{}})
	if status != http.StatusOK {
		t.Fatalf("run with omitted default status = %d, want 200, body: %v", status, resp)
	}
	_, stored = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, userID),
		map[string]any{"sql": "SELECT started_at FROM sessions WHERE started_at LIKE '2026-08-10%'"})
	rows, _ = stored["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("defaulted rows = %d, want 1, body: %v", len(rows), stored)
	}
	if got := extractRowValues(t, rows[0])[0]; got != "2026-08-10 08:00:00" {
		t.Errorf("stored default = %v, want the canonical 2026-08-10 08:00:00", got)
	}
}

// TestFormulas_REST_TimeParam pins the bare clock-time vertical: "HH:MM" input gains ":00" seconds so
// one column holds one shape, and what is not a 24-hour clock time is refused at the boundary.
func TestFormulas_REST_TimeParam(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE reminders (id INTEGER PRIMARY KEY, ring_at TEXT)"})

	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "add-reminder", "description": "Records a daily reminder's ring time.",
		"sql":    "INSERT INTO reminders (ring_at) VALUES (:ring_at)",
		"params": []map[string]any{{"name": "ring_at", "type": "time", "source": "prompt"}},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-reminder"),
		userToken(t, userID), map[string]any{"params": map[string]any{"ring_at": "7:30"}})
	if status != http.StatusOK {
		t.Fatalf("run status = %d, want 200, body: %v", status, resp)
	}

	for _, bad := range []any{"24:00", "14:61", "9pm", "2026-08-09 14:30", 730} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-reminder"),
			userToken(t, userID), map[string]any{"params": map[string]any{"ring_at": bad}})
		if status != http.StatusBadRequest {
			t.Errorf("run with %v: status = %d, want 400, body: %v", bad, status, resp)
		}
	}

	_, stored := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, userID),
		map[string]any{"sql": "SELECT ring_at FROM reminders"})
	rows, _ := stored["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("stored rows = %d, want only the valid time, body: %v", len(rows), stored)
	}
	if got := extractRowValues(t, rows[0])[0]; got != "07:30:00" {
		t.Errorf("stored time = %v, want the canonical 07:30:00", got)
	}
}

// --- Row-detail attachment (a view attached to a view) ---

// TestFormulas_REST_AttachDetailView pins the row-detail lifecycle: a view declaring :rowid (plus
// an optional prompt) attaches to a list view, runs from a row id, and cascades on parent delete.
func TestFormulas_REST_AttachDetailView(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "groceries-list",
		"description": "All groceries.",
		"sql":         "SELECT rowid AS rowid, name FROM groceries ORDER BY rowid",
	})

	detail := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "grocery-detail",
		"description":            "One grocery, all columns.",
		"attached_to_formula_id": view["id"],
		"sql": "SELECT id, name, quantity FROM groceries WHERE rowid = :rowid " +
			"AND (:min_qty IS NULL OR quantity >= :min_qty)",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "min_qty", "type": "integer", "source": "prompt", "optional": true},
		},
	})
	if detail["kind"] != "view" {
		t.Errorf("detail kind = %v, want view", detail["kind"])
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "grocery-detail"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"rowid": 1}})
	if status != http.StatusOK {
		t.Fatalf("run detail view status = %d, want 200, body: %v", status, resp)
	}
	rows, _ := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("detail rows = %d, want 1", len(rows))
	}
	if name := extractRowValues(t, rows[0])[1]; name != "milk" {
		t.Errorf("detail row name = %v, want milk (rowid 1)", name)
	}

	// Deleting the parent view cascades to the attached detail.
	status, _ = testutil.DoRequest(t, router, http.MethodDelete,
		formulasPath(bdbID, "groceries-list"), userToken(t, userID), nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete parent view status = %d, want 204", status)
	}
	status, _ = testutil.DoRequest(t, router, http.MethodGet, formulasPath(bdbID, "grocery-detail"),
		userToken(t, userID), nil)
	if status != http.StatusNotFound {
		t.Errorf("get cascaded detail status = %d, want 404", status)
	}
}

// TestFormulas_REST_AttachDetailViewParamRules pins the row-detail admission rule: the attached
// view must declare :rowid, any other param must be optional, and an update may not break either.
func TestFormulas_REST_AttachDetailViewParamRules(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "groceries-list",
		"description": "All groceries.",
		"sql":         "SELECT rowid AS rowid, name FROM groceries ORDER BY rowid",
	})

	// Missing :rowid.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), map[string]any{
			"name": "no-rowid", "description": "x",
			"sql":                    "SELECT name FROM groceries",
			"attached_to_formula_id": view["id"],
		})
	if status != http.StatusBadRequest {
		t.Errorf("detail without :rowid: status = %d, want 400, body: %v", status, resp)
	}

	// A required prompt param cannot bind from a row click.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), map[string]any{
			"name": "required-prompt", "description": "x",
			"sql": "SELECT name FROM groceries WHERE rowid = :rowid AND quantity >= :min",
			"params": []map[string]any{
				{"name": "rowid", "type": "integer", "source": "selection-single"},
				{"name": "min", "type": "integer", "source": "prompt"},
			},
			"attached_to_formula_id": view["id"],
		})
	if status != http.StatusBadRequest {
		t.Errorf("detail with required prompt: status = %d, want 400, body: %v", status, resp)
	}

	// A valid detail cannot be updated out of the row-detail shape while attached.
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name": "grocery-detail", "description": "x",
		"sql":                    "SELECT name FROM groceries WHERE rowid = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": view["id"],
	})
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-detail"),
		userToken(t, userID), map[string]any{
			"sql":    "SELECT name FROM groceries",
			"params": []map[string]any{},
		})
	if status != http.StatusBadRequest {
		t.Errorf("update dropping :rowid while attached: status = %d, want 400, body: %v", status, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-detail"),
		userToken(t, userID), map[string]any{
			"sql": "DELETE FROM groceries WHERE rowid = :rowid",
		})
	if status != http.StatusBadRequest {
		t.Errorf("attached detail flipping to DML: status = %d, want 400, body: %v", status, resp)
	}
}

// TestFormulas_REST_MultipleRoundTripAndRejections pins the multi-select flag's wire contract: a
// created multiple lookup reads back with multiple: true and its "all" default, while the
// create-time guards reject the flag off a lookup, next to allow_new, and with any other default.
func TestFormulas_REST_MultipleRoundTripAndRejections(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createInventoryTable(t, router, bdbID, userID)

	created := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "by-suppliers",
		"description": "Inventory narrowed to the picked suppliers.",
		"sql": "SELECT rowid, * FROM inventory " +
			"WHERE supplier IN (SELECT je.value FROM json_each(:suppliers) je)",
		"params": []map[string]any{{
			"name": "suppliers", "type": "text", "source": "lookup", "multiple": true, "default": "all",
			"lookup": map[string]any{"table": "inventory", "label_column": "supplier", "value_column": "supplier"},
		}},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodGet,
		formulasPath(bdbID, created["id"].(string)), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200, body: %v", status, resp)
	}
	params, _ := resp["params"].([]any)
	suppliers, _ := params[0].(map[string]any)
	if suppliers["multiple"] != true || suppliers["default"] != "all" {
		t.Errorf("suppliers param = %v, want multiple: true with default all", suppliers)
	}

	for _, tt := range []struct {
		label string
		param map[string]any
	}{
		{"multiple on a prompt param", map[string]any{
			"name": "suppliers", "type": "text", "source": "prompt", "multiple": true,
		}},
		{"multiple with allow_new", map[string]any{
			"name": "suppliers", "type": "text", "source": "lookup", "multiple": true,
			"lookup": map[string]any{"table": "inventory", "label_column": "supplier",
				"value_column": "supplier", "allow_new": true},
		}},
		{"a multi default other than all", map[string]any{
			"name": "suppliers", "type": "text", "source": "lookup", "multiple": true, "default": "acme",
			"lookup": map[string]any{"table": "inventory", "label_column": "supplier",
				"value_column": "supplier"},
		}},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
			userToken(t, userID), map[string]any{
				"name":        "rejected",
				"description": "Never stored.",
				"sql":         "SELECT rowid, * FROM inventory WHERE :suppliers IS NOT NULL",
				"params":      []map[string]any{tt.param},
			})
		if status != http.StatusBadRequest {
			t.Errorf("%s: create status = %d, want 400, body: %v", tt.label, status, resp)
		}
	}
}
