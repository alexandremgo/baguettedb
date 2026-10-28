//go:build integration

package integrationtest

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
)

// applyRecipesMealPlan applies the embedded recipes-meal-plan starter for a fresh user and returns the wired
// services with the new bdb's coordinates, ready for formula runs.
func applyRecipesMealPlan(t *testing.T) (deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	userID = uuid.New()
	bdbID, err = svc.CreateFromStarter(t.Context(), userID, testRoles, "recipes-meal-plan", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter(recipes-meal-plan) error = %v", err)
	}
	return deps, userID, bdbID
}

// slotNames reads the slot vocabulary in position order.
func slotNames(t *testing.T, deps engineDeps, userID, bdbID uuid.UUID) []string {
	t.Helper()
	res := execSQL(t, deps, userID, bdbID, "SELECT name FROM meal_slots ORDER BY position, id", dbdomain.IntentQuery)
	out := make([]string, 0, len(res.Rows))
	for i := range res.Rows {
		out = append(out, cell(t, res, i, "name").(string))
	}
	return out
}

// week is the window the meal-plan tests read: the ISO week holding the planned sample days.
var week = map[string]any{"window_start": "2026-09-07", "window_end": "2026-09-13"}

// TestRecipesSlotsSeededAndCreatedOnTheFly pins the slot vocabulary: the seed lands in its order, a slot typed
// past the suggestions is appended last, and the same slot typed in another case reuses it.
func TestRecipesSlotsSeededAndCreatedOnTheFly(t *testing.T) {
	deps, userID, bdbID := applyRecipesMealPlan(t)

	want := []string{"breakfast", "lunch", "dinner", "snack"}
	if got := slotNames(t, deps, userID, bdbID); !slices.Equal(got, want) {
		t.Fatalf("seeded slots = %v, want %v", got, want)
	}

	runFormula(t, deps, userID, bdbID, "add-meal", map[string]any{"meal": "Quiche", "slot": "brunch"})
	runFormula(t, deps, userID, bdbID, "add-meal", map[string]any{"meal": "Omelette", "slot": " Brunch "})
	want = append(want, "brunch")
	if got := slotNames(t, deps, userID, bdbID); !slices.Equal(got, want) {
		t.Errorf("slots after two brunch meals = %v, want %v — one appended, the second reused", got, want)
	}
	meals := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM meals WHERE slot_id = (SELECT id FROM meal_slots WHERE name = 'brunch')",
		dbdomain.IntentQuery)
	if n := cell(t, meals, 0, "n"); n != int64(2) {
		t.Errorf("meals in brunch = %v, want both", n)
	}
}

// TestRecipesTagsRoundTrip drives the tags group end to end: blank rows are dropped and the rest stored as a
// string array, the tag picker's source lists them, the cards' tag filter narrows on one, and an edit that
// passes fewer tags removes the others.
func TestRecipesTagsRoundTrip(t *testing.T) {
	deps, userID, bdbID := applyRecipesMealPlan(t)

	runFormula(t, deps, userID, bdbID, "add-recipe", map[string]any{
		"name": "Pumpkin soup",
		"tags": []any{map[string]any{"tag": "soup"}, map[string]any{"tag": " "}, map[string]any{"tag": " autumn "}},
	})
	runFormula(t, deps, userID, bdbID, "add-recipe", map[string]any{
		"name": "Pancakes", "tags": []any{map[string]any{"tag": "quick"}},
	})

	stored := execSQL(t, deps, userID, bdbID,
		"SELECT tags FROM recipes WHERE name = 'Pumpkin soup'", dbdomain.IntentQuery)
	if tags := cell(t, stored, 0, "tags"); tags != `["soup","autumn"]` {
		t.Errorf("stored tags = %v, want the two non-blank tags, trimmed", tags)
	}
	options := execSQL(t, deps, userID, bdbID, "SELECT tag FROM tag_options ORDER BY tag", dbdomain.IntentQuery)
	if len(options.Rows) != 3 {
		t.Errorf("tag_options = %v, want autumn, quick, soup", options.Rows)
	}

	filtered := runFormula(t, deps, userID, bdbID, "recipe-cards", map[string]any{"tag": "soup"})
	if len(filtered.Result.Rows) != 1 || cell(t, filtered.Result, 0, "name") != "Pumpkin soup" {
		t.Errorf("cards filtered by tag = %v, want the soup alone", filtered.Result.Rows)
	}
	// The cards' body carries the tags as a hashtag line, and the edit seed reshapes them as {tag} rows.
	if body := cell(t, filtered.Result, 0, "body").(string); !strings.Contains(body, "#soup #autumn") {
		t.Errorf("card body = %q, want a #soup #autumn line", body)
	}
	if seed := cell(t, filtered.Result, 0, "tags"); seed != `[{"tag":"soup"},{"tag":"autumn"}]` {
		t.Errorf("edit seed tags = %v, want {tag} objects", seed)
	}

	runFormula(t, deps, userID, bdbID, "edit-recipe", map[string]any{
		"rowid": float64(1), "name": "Pumpkin soup", "tags": []any{map[string]any{"tag": "soup"}},
	})
	stored = execSQL(t, deps, userID, bdbID, "SELECT tags FROM recipes WHERE id = 1", dbdomain.IntentQuery)
	if tags := cell(t, stored, 0, "tags"); tags != `["soup"]` {
		t.Errorf("tags after edit = %v, want autumn removed", tags)
	}
}

// TestMealPlanWindowTrayAndMoves drives the calendar's contract through the seeded formulas: a planned recipe
// and an undated dish both show in the week (the tray survives the window), only the tray shows in another
// week, a move re-homes the day, unplanning sends a meal to the tray, and a dish typed as a recipe's name
// links the recipe instead of storing text.
func TestMealPlanWindowTrayAndMoves(t *testing.T) {
	deps, userID, bdbID := applyRecipesMealPlan(t)

	runFormula(t, deps, userID, bdbID, "add-recipe", map[string]any{"name": "Pumpkin soup"})
	runFormula(t, deps, userID, bdbID, "plan-recipe", map[string]any{
		"rowid": float64(1), "day": "2026-09-07", "slot": "dinner", "notes": "double batch",
	})
	runFormula(t, deps, userID, bdbID, "add-meal", map[string]any{"meal": "Quiche"})
	runFormula(t, deps, userID, bdbID, "add-meal", map[string]any{
		"meal": "pumpkin SOUP", "day": "2026-09-09", "slot": "lunch",
	})

	titles := func(params map[string]any) []string {
		res := runFormula(t, deps, userID, bdbID, "meal-plan", params)
		out := make([]string, 0, len(res.Result.Rows))
		for i := range res.Result.Rows {
			out = append(out, cell(t, res.Result, i, "title").(string))
		}
		return out
	}
	if got, want := titles(week), []string{"Quiche", "Pumpkin soup", "Pumpkin soup"}; !slices.Equal(got, want) {
		t.Errorf("week titles = %v, want %v — the tray first, then the days", got, want)
	}
	nextWeek := map[string]any{"window_start": "2026-09-14", "window_end": "2026-09-20"}
	if got, want := titles(nextWeek), []string{"Quiche"}; !slices.Equal(got, want) {
		t.Errorf("next week titles = %v, want %v — the tray alone", got, want)
	}
	// The Wednesday soup was typed by name in another case: it links the recipe, so nothing is stored as
	// a dish and its body names the slot.
	linked := execSQL(t, deps, userID, bdbID,
		"SELECT recipe_id, dish FROM meals WHERE plan_date = '2026-09-09'", dbdomain.IntentQuery)
	if cell(t, linked, 0, "recipe_id") != int64(1) || cell(t, linked, 0, "dish") != nil {
		t.Errorf("meal typed as a recipe name = %v, want the recipe linked and no dish", linked.Rows)
	}
	planned := runFormula(t, deps, userID, bdbID, "meal-plan", week)
	if body := cell(t, planned.Result, 1, "body"); body != "dinner — double batch" {
		t.Errorf("planned soup body = %v, want the slot and the note", body)
	}

	runFormula(t, deps, userID, bdbID, "move-meal", map[string]any{"rowid": float64(1), "day": "2026-09-08"})
	moved := execSQL(t, deps, userID, bdbID, "SELECT plan_date FROM meals WHERE id = 1", dbdomain.IntentQuery)
	if day := cell(t, moved, 0, "plan_date"); day != "2026-09-08" {
		t.Errorf("day after move = %v, want 2026-09-08", day)
	}
	runFormula(t, deps, userID, bdbID, "unplan-meal", map[string]any{"rowid": float64(1)})
	unplanned := execSQL(t, deps, userID, bdbID,
		"SELECT plan_date, slot_id FROM meals WHERE id = 1", dbdomain.IntentQuery)
	if cell(t, unplanned, 0, "plan_date") != nil || cell(t, unplanned, 0, "slot_id") == nil {
		t.Errorf("unplanned meal = %v, want no day and the slot kept", unplanned.Rows)
	}
}

// TestMealPlanDayOrdering pins the order within a day: slot position, unslotted meals last, insertion order
// inside a slot — the calendar keeps the view's order, so this is what a day reads like.
func TestMealPlanDayOrdering(t *testing.T) {
	deps, userID, bdbID := applyRecipesMealPlan(t)

	add := func(meal, slot string) {
		runFormula(t, deps, userID, bdbID, "add-meal", map[string]any{"meal": meal, "day": "2026-09-08", "slot": slot})
	}
	add("Stew", "dinner")
	add("Apple", "")
	add("Toast", "breakfast")
	add("Cheese", "dinner")

	res := runFormula(t, deps, userID, bdbID, "meal-plan", week)
	got := make([]string, 0, len(res.Result.Rows))
	for i := range res.Result.Rows {
		got = append(got, cell(t, res.Result, i, "title").(string))
	}
	if want := []string{"Toast", "Stew", "Cheese", "Apple"}; !slices.Equal(got, want) {
		t.Errorf("day order = %v, want %v", got, want)
	}
}

// TestRecipesCookingHistoryAndBatches drives the history rules: a cook logged today counts, servings planned
// from a batch do not, deleting the batch deletes its servings, and deleting a recipe leaves its meals with the
// name as their dish.
func TestRecipesCookingHistoryAndBatches(t *testing.T) {
	deps, userID, bdbID := applyRecipesMealPlan(t)
	today := time.Now().Format("2006-01-02")

	runFormula(t, deps, userID, bdbID, "add-recipe", map[string]any{"name": "Pumpkin soup"})
	runFormula(t, deps, userID, bdbID, "cooked-it", map[string]any{"rowid": float64(1)})
	runFormula(t, deps, userID, bdbID, "plan-recipe", map[string]any{
		"rowid": float64(1), "day": "2026-09-07", "slot": "dinner",
	})
	runFormula(t, deps, userID, bdbID, "serve-again", map[string]any{
		"rowid": float64(2),
		"days":  []any{map[string]any{"day": "2026-09-08"}, map[string]any{"day": "2026-09-09"}},
	})

	detail := runFormula(t, deps, userID, bdbID, "recipe-detail", map[string]any{"rowid": float64(1)})
	if n := cell(t, detail.Result, 0, "times_cooked"); n != int64(2) {
		t.Errorf("times_cooked = %v, want 2 — today's cook and the batch, not the servings", n)
	}
	if last := cell(t, detail.Result, 0, "last_cooked"); last != today {
		t.Errorf("last_cooked = %v, want today (%s)", last, today)
	}
	servings := execSQL(t, deps, userID, bdbID,
		"SELECT plan_date, slot_id, from_meal_id FROM meals WHERE from_meal_id IS NOT NULL ORDER BY plan_date",
		dbdomain.IntentQuery)
	if len(servings.Rows) != 2 {
		t.Fatalf("servings = %v, want one per listed day", servings.Rows)
	}
	if cell(t, servings, 0, "from_meal_id") != int64(2) || cell(t, servings, 0, "slot_id") != int64(3) {
		t.Errorf("first serving = %v, want it linked to the batch in the batch's slot", servings.Rows[0])
	}
	plan := runFormula(t, deps, userID, bdbID, "meal-plan", week)
	if body := cell(t, plan.Result, 1, "body"); body != "dinner — made 2026-09-07" {
		t.Errorf("serving body = %v, want the slot and the day it was made", body)
	}
	// A serving of a serving still points at the original batch.
	runFormula(t, deps, userID, bdbID, "serve-again", map[string]any{
		"rowid": float64(3), "days": []any{map[string]any{"day": "2026-09-10"}},
	})
	chained := execSQL(t, deps, userID, bdbID,
		"SELECT from_meal_id FROM meals WHERE plan_date = '2026-09-10'", dbdomain.IntentQuery)
	if cell(t, chained, 0, "from_meal_id") != int64(2) {
		t.Errorf("serving of a serving points at %v, want the batch (2)", cell(t, chained, 0, "from_meal_id"))
	}

	runFormula(t, deps, userID, bdbID, "delete-meal", map[string]any{"rowid": float64(2)})
	left := execSQL(t, deps, userID, bdbID, "SELECT COUNT(*) AS n FROM meals", dbdomain.IntentQuery)
	if n := cell(t, left, 0, "n"); n != int64(1) {
		t.Errorf("meals after deleting the batch = %v, want today's cook alone — the servings cascade", n)
	}

	runFormula(t, deps, userID, bdbID, "delete-recipe", map[string]any{"rowid": float64(1)})
	orphan := execSQL(t, deps, userID, bdbID, "SELECT recipe_id, dish FROM meals", dbdomain.IntentQuery)
	if cell(t, orphan, 0, "recipe_id") != nil || cell(t, orphan, 0, "dish") != "Pumpkin soup" {
		t.Errorf("meal after deleting its recipe = %v, want the link cleared, the name kept as dish", orphan.Rows)
	}
}

// TestRecipesSlotVocabularyActions drives the assistant-only slot actions: a reorder from a partial list
// leads with the named slots and keeps the rest in their relative order behind them, and a rename follows
// through to the meals in that slot.
func TestRecipesSlotVocabularyActions(t *testing.T) {
	deps, userID, bdbID := applyRecipesMealPlan(t)

	runFormula(t, deps, userID, bdbID, "reorder-slots", map[string]any{"names": `["dinner", "Breakfast"]`})
	want := []string{"dinner", "breakfast", "lunch", "snack"}
	if got := slotNames(t, deps, userID, bdbID); !slices.Equal(got, want) {
		t.Errorf("slots after reorder = %v, want %v", got, want)
	}

	runFormula(t, deps, userID, bdbID, "add-meal", map[string]any{
		"meal": "Toast", "day": "2026-09-08", "slot": "breakfast",
	})
	runFormula(t, deps, userID, bdbID, "rename-slot", map[string]any{"slot": "breakfast", "new_name": "morning"})
	plan := runFormula(t, deps, userID, bdbID, "meal-plan", week)
	if slot := cell(t, plan.Result, 0, "slot"); slot != "morning" {
		t.Errorf("meal slot after rename = %v, want morning", slot)
	}
}

// TestRecipesDisplayWiring pins the two surfaces' display configs and the creatable pickers behind them: the
// cards' slots and row menu, the calendar's mode, day role, window pair, slots and row menu, the inline meal
// field, and the slot pickers that create what is typed.
func TestRecipesDisplayWiring(t *testing.T) {
	deps, userID, bdbID := applyRecipesMealPlan(t)

	formulas, err := deps.formulaSvc.ListFormulasIncludingHidden(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulasIncludingHidden() error = %v", err)
	}
	byName := make(map[string]*dbdomain.Formula, len(formulas))
	for _, f := range formulas {
		byName[f.Name] = f
	}
	id := func(name string) string { return byName[name].ID.String() }

	cards := byName["recipe-cards"]
	if cards.Display == nil || cards.Display.Mode != dbdomain.DisplayModeCards {
		t.Fatalf("recipe-cards display = %+v, want cards mode", cards.Display)
	}
	if s := cards.Display.Slots; s.Add != id("add-recipe") || s.Edit != id("edit-recipe") ||
		s.Delete != id("delete-recipe") || s.Detail != id("recipe-detail") {
		t.Errorf("recipe-cards slots = %+v, want add/edit/delete/detail wired", s)
	}
	if want := []string{id("plan-recipe"), id("cooked-it")}; !slices.Equal(cards.Display.Slots.RowActions, want) {
		t.Errorf("recipe-cards row actions = %v, want plan-recipe leading cooked-it", cards.Display.Slots.RowActions)
	}

	plan := byName["meal-plan"]
	if plan.Display == nil || plan.Display.Mode != dbdomain.DisplayModeCalendar {
		t.Fatalf("meal-plan display = %+v, want calendar mode", plan.Display)
	}
	var dayColumn string
	for _, c := range plan.Display.Columns {
		if c.Role == dbdomain.DisplayRoleDay {
			dayColumn = c.Column
		}
	}
	if dayColumn != "day" {
		t.Errorf("meal-plan day-role column = %q, want day", dayColumn)
	}
	for _, name := range []string{"window_start", "window_end"} {
		found := false
		for _, p := range plan.Params {
			found = found ||
				(p.Name == name && p.Source == dbdomain.ParamSourceWindow && p.Type == dbdomain.ParamTypeDate)
		}
		if !found {
			t.Errorf("meal-plan lacks the %s date window param", name)
		}
	}
	if s := plan.Display.Slots; s.Add != id("add-meal") || s.Move != id("move-meal") || s.Edit != id("edit-meal") ||
		s.Delete != id("delete-meal") || s.Detail != id("meal-detail") || s.Reorder != "" {
		t.Errorf("meal-plan slots = %+v, want add/move/edit/delete/detail wired and no reorder", s)
	}
	if want := []string{id("serve-again"), id("unplan-meal")}; !slices.Equal(plan.Display.Slots.RowActions, want) {
		t.Errorf("meal-plan row actions = %v, want serve-again leading unplan-meal", plan.Display.Slots.RowActions)
	}

	// The calendar's quick-add bar is the meal field alone; the day (seeded by a day's "+"), slot and
	// notes wait in the dialog.
	for _, p := range byName["add-meal"].Params {
		if p.Inline != (p.Name == "meal") {
			t.Errorf("add-meal param %s inline = %v, want only meal inline", p.Name, p.Inline)
		}
	}
	creatable := []struct{ formula, param, table string }{
		{"add-meal", "meal", "recipe_options"},
		{"edit-meal", "meal", "recipe_options"},
		{"add-meal", "slot", "slot_options"},
		{"edit-meal", "slot", "slot_options"},
		{"plan-recipe", "slot", "slot_options"},
		{"serve-again", "slot", "slot_options"},
	}
	for _, want := range creatable {
		var found *dbdomain.FormulaParam
		for i := range byName[want.formula].Params {
			if byName[want.formula].Params[i].Name == want.param {
				found = &byName[want.formula].Params[i]
			}
		}
		if found == nil || found.Lookup == nil || !found.Lookup.AllowNew || found.Lookup.Table != want.table {
			t.Errorf("%s %s param = %+v, want an allow_new lookup over %s",
				want.formula, want.param, found, want.table)
		}
	}
	for _, name := range []string{"reorder-slots", "rename-slot"} {
		if !byName[name].Hidden {
			t.Errorf("%s is listed, want it hidden — assistant-only", name)
		}
	}
}
