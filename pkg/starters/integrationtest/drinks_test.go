//go:build integration

package integrationtest

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
)

// applyDrinks applies the embedded drinks starter for a fresh user and returns the wired services
// with the new bdb's coordinates, ready for formula runs.
func applyDrinks(t *testing.T) (deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	userID = uuid.New()
	bdbID, err = svc.CreateFromStarter(t.Context(), userID, testRoles, "drinks", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter(drinks) error = %v", err)
	}
	return deps, userID, bdbID
}

// TestDrinksSurface pins the seeded shape: four cards views with their slot sets (Top rated
// deliberately add-less), the quick bar exposing only name and kind, the per-kind fields gated on the
// kind choice, and the ingredient editor nested behind the recipe detail with its reorder slot.
func TestDrinksSurface(t *testing.T) {
	deps, userID, bdbID := applyDrinks(t)

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	byName := make(map[string]*dbdomain.Formula, len(formulas))
	for _, f := range formulas {
		byName[f.Name] = f
	}

	for _, name := range []string{"tastings", "cellar", "top-rated", "recipe-book"} {
		f := byName[name]
		if f == nil || f.Display == nil || f.Display.Mode != dbdomain.DisplayModeCards {
			t.Fatalf("%s display = %+v, want cards mode", name, f)
		}
	}
	if slots := byName["tastings"].Display.Slots; slots.Add == "" || slots.Edit == "" ||
		slots.Delete == "" || slots.Detail == "" || len(slots.RowActions) != 1 {
		t.Errorf("tastings slots = %+v, want add/edit/delete/detail and the wishlist row action", slots)
	}
	if slots := byName["cellar"].Display.Slots; slots.Add == "" || slots.Edit == "" ||
		slots.Delete == "" || len(slots.RowActions) != 3 {
		t.Errorf("cellar slots = %+v, want add/edit/delete and three row actions", slots)
	}
	if slots := byName["top-rated"].Display.Slots; slots.Add != "" {
		t.Errorf("top-rated add slot = %q, want none — the catalog only grows through activity", slots.Add)
	}
	if slots := byName["recipe-detail"].Display.Slots; slots.Add == "" || slots.Edit == "" ||
		slots.Delete == "" || slots.Reorder == "" {
		t.Errorf("recipe-detail slots = %+v, want the full ingredient editor with reorder", slots)
	}

	gates := map[string]string{"grape": "wine", "vintage": "wine", "roast": "coffee", "origin": "coffee",
		"dose_g": "coffee", "grind": "coffee", "method": "coffee", "base_spirit": "cocktail", "style": "beer"}
	for _, p := range byName["log-tasting"].Params {
		if inline := p.Name == "name" || p.Name == "kind"; p.Inline != inline {
			t.Errorf("log-tasting %s inline = %v, want the quick bar to carry name and kind only", p.Name, p.Inline)
		}
		if kind, gated := gates[p.Name]; gated && (p.When == nil || p.When.Param != "kind" || p.When.Value != kind) {
			t.Errorf("log-tasting %s when = %+v, want gated on kind = %s", p.Name, p.When, kind)
		}
	}

	// Both drink pickers are scoped to the selected kind, so the options can never disagree with the
	// dropdown next to them.
	for _, name := range []string{"log-tasting", "add-batch"} {
		for _, p := range byName[name].Params {
			if p.Name == "name" && (p.Lookup == nil || p.Lookup.Scope["kind"] != "kind") {
				t.Errorf("%s name lookup scope = %+v, want options narrowed by the kind param", name, p.Lookup)
			}
		}
	}
}

// TestDrinksLogAndDedupe drives the catalog contract: a re-tasting reuses the drink's row, later logs
// fill blank facts without overwriting known ones, and the cards render the half-star rating, the NA
// marker, and a coffee's brew line.
func TestDrinksLogAndDedupe(t *testing.T) {
	deps, userID, bdbID := applyDrinks(t)

	runFormula(t, deps, userID, bdbID, "log-tasting",
		map[string]any{"name": "Chinon 2019", "kind": "wine", "rating": 4.5, "grape": "cabernet franc"})
	runFormula(t, deps, userID, bdbID, "log-tasting",
		map[string]any{"name": "Chinon 2019", "kind": "wine", "rating": 3.5, "maker": "Domaine B."})
	counts := execSQL(t, deps, userID, bdbID,
		"SELECT (SELECT COUNT(*) FROM drinks) AS drinks, (SELECT COUNT(*) FROM tastings) AS tastings, "+
			"(SELECT maker FROM drinks) AS maker, (SELECT grape FROM drinks) AS grape", dbdomain.IntentQuery)
	if cell(t, counts, 0, "drinks") != int64(1) || cell(t, counts, 0, "tastings") != int64(2) {
		t.Fatalf("after re-tasting = %v, want one drink with two tastings", counts.Rows)
	}
	if cell(t, counts, 0, "maker") != "Domaine B." || cell(t, counts, 0, "grape") != "cabernet franc" {
		t.Errorf("drink facts = %v, want the second log filling maker while grape survives", counts.Rows)
	}
	runFormula(t, deps, userID, bdbID, "log-tasting",
		map[string]any{"name": "Chinon 2019", "kind": "wine", "maker": "Someone Else"})
	if kept := execSQL(t, deps, userID, bdbID, "SELECT maker FROM drinks",
		dbdomain.IntentQuery); cell(t, kept, 0, "maker") != "Domaine B." {
		t.Errorf("maker after a conflicting log = %v, want fill-blank-only, never overwrite", kept.Rows)
	}

	// A typed case variant lands on the same drink, and the stored spelling survives.
	runFormula(t, deps, userID, bdbID, "log-tasting", map[string]any{"name": "chinon 2019", "kind": "wine"})
	spelling := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n, MAX(name) AS name FROM drinks", dbdomain.IntentQuery)
	if cell(t, spelling, 0, "n") != int64(1) || cell(t, spelling, 0, "name") != "Chinon 2019" {
		t.Errorf("after a case-variant log = %v, want one drink keeping its canonical spelling", spelling.Rows)
	}

	feed := runFormula(t, deps, userID, bdbID, "tastings", nil)
	if body, _ := cell(t, feed.Result, 3, "body").(string); !strings.HasPrefix(body, "★★★★½") {
		t.Errorf("first tasting body = %q, want the half-star render leading", body)
	}

	runFormula(t, deps, userID, bdbID, "log-tasting",
		map[string]any{"name": "Ethiopia Guji", "kind": "coffee", "non_alcoholic": true,
			"dose_g": 18.0, "grind": "12", "method": "V60"})
	feed = runFormula(t, deps, userID, bdbID, "tastings", map[string]any{"kind": "coffee"})
	if heading, _ := cell(t, feed.Result, 0, "heading").(string); !strings.Contains(heading, "☕ Ethiopia Guji · 0.0%") {
		t.Errorf("coffee heading = %q, want the emoji and the NA marker", heading)
	}
	if body, _ := cell(t, feed.Result, 0, "body").(string); !strings.Contains(body, "⚙️ 18 g · grind 12 · V60") {
		t.Errorf("coffee body = %q, want the composed brew line", body)
	}
}

// TestDrinksCellar drives the per-batch loop: an owned batch renders its quantity and flips its expiry
// marker once past, a wish for a stocked drink reads as a restock, got-it turns a wish into a batch
// dated today, and finishing hides the row without touching tastings.
func TestDrinksCellar(t *testing.T) {
	deps, userID, bdbID := applyDrinks(t)

	runFormula(t, deps, userID, bdbID, "add-batch",
		map[string]any{"name": "Chinon 2019", "kind": "wine", "quantity": 2.0, "unit": "bottles",
			"expires_on": "2020-01-01"})
	view := runFormula(t, deps, userID, bdbID, "cellar", nil)
	if body, _ := cell(t, view.Result, 0, "body").(string); !strings.Contains(body, "📦 2 bottles") ||
		!strings.Contains(body, "⚠️ expired 2020-01-01") {
		t.Errorf("owned batch body = %q, want the quantity and the expired flip", body)
	}

	// A wish for the same drink: reuses the drink row and reads as a restock below the owned batch.
	runFormula(t, deps, userID, bdbID, "add-batch",
		map[string]any{"name": "Chinon 2019", "kind": "wine", "status": "wishlist"})
	view = runFormula(t, deps, userID, bdbID, "cellar", nil)
	if len(view.Result.Rows) != 2 {
		t.Fatalf("cellar rows = %d, want the batch and the wish", len(view.Result.Rows))
	}
	if body, _ := cell(t, view.Result, 1, "body").(string); !strings.Contains(body, "🛒 wanted since") ||
		!strings.Contains(body, "📦 also in the cellar") {
		t.Errorf("wish body = %q, want the wishlist line with the restock marker", body)
	}
	if drinks := execSQL(t, deps, userID, bdbID, "SELECT COUNT(*) AS n FROM drinks",
		dbdomain.IntentQuery); cell(t, drinks, 0, "n") != int64(1) {
		t.Errorf("drinks after the wish = %v, want the existing row reused", drinks.Rows)
	}

	wishID := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM cellar WHERE status = 'wishlist'", dbdomain.IntentQuery), 0, "id").(int64)
	runFormula(t, deps, userID, bdbID, "got-it",
		map[string]any{"rowid": float64(wishID), "quantity": 6.0, "unit": "bottles"})
	got := execSQL(t, deps, userID, bdbID,
		"SELECT status, quantity, added_on = date('now') AS today FROM cellar WHERE id = "+
			"(SELECT MAX(id) FROM cellar)", dbdomain.IntentQuery)
	if cell(t, got, 0, "status") != "owned" || cell(t, got, 0, "quantity") != 6.0 ||
		cell(t, got, 0, "today") != int64(1) {
		t.Errorf("after got-it = %v, want an owned batch of 6 dated today", got.Rows)
	}

	runFormula(t, deps, userID, bdbID, "log-from-cellar", map[string]any{"rowid": float64(wishID), "rating": 4.0})
	if quantity := execSQL(t, deps, userID, bdbID, "SELECT quantity FROM cellar WHERE id = "+
		"(SELECT MAX(id) FROM cellar)", dbdomain.IntentQuery); cell(t, quantity, 0, "quantity") != 6.0 {
		t.Errorf("quantity after a tasting = %v, want untouched — tastings never decrement", quantity.Rows)
	}

	runFormula(t, deps, userID, bdbID, "finish-batch", map[string]any{"rowid": float64(wishID)})
	view = runFormula(t, deps, userID, bdbID, "cellar", nil)
	if len(view.Result.Rows) != 1 {
		t.Errorf("cellar rows after finishing = %d, want the finished batch hidden", len(view.Result.Rows))
	}
	if tastings := execSQL(t, deps, userID, bdbID, "SELECT COUNT(*) AS n FROM tastings",
		dbdomain.IntentQuery); cell(t, tastings, 0, "n") != int64(1) {
		t.Errorf("tastings after finishing = %v, want the log kept", tastings.Rows)
	}
}

// TestDrinksTopRatedAndRecipes pins the aggregate face and the recipe book: averages render in
// half-stars with the tasting count and the min-rating filter cuts on them, and a recipe carries its
// grouped ingredient lines through add, reorder, and the log that creates its catalog drink.
func TestDrinksTopRatedAndRecipes(t *testing.T) {
	deps, userID, bdbID := applyDrinks(t)

	runFormula(t, deps, userID, bdbID, "log-tasting", map[string]any{"name": "Guji", "kind": "coffee", "rating": 4.0})
	drinkID := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM drinks", dbdomain.IntentQuery), 0, "id").(int64)
	runFormula(t, deps, userID, bdbID, "log-again", map[string]any{"rowid": float64(drinkID), "rating": 5.0})

	top := runFormula(t, deps, userID, bdbID, "top-rated", nil)
	if body, _ := cell(t, top.Result, 0, "body").(string); !strings.Contains(body, "★★★★½ 4.5") ||
		!strings.Contains(body, "tasted 2 times") {
		t.Errorf("top-rated body = %q, want the averaged half-stars with the count", body)
	}
	if filtered := runFormula(t, deps, userID, bdbID, "top-rated",
		map[string]any{"min_rating": 4.75}); len(filtered.Result.Rows) != 0 {
		t.Errorf("min-rating filter rows = %d, want the 4.5 average cut off", len(filtered.Result.Rows))
	}

	runFormula(t, deps, userID, bdbID, "add-recipe", map[string]any{
		"name": "Gimlet", "kind": "cocktail", "method": "Shake with ice, strain.",
		"ingredients": []any{
			map[string]any{"amount": "20 ml", "item": "lime cordial"},
			map[string]any{"amount": "50 ml", "item": "gin"},
		}})
	book := runFormula(t, deps, userID, bdbID, "recipe-book", nil)
	if body, _ := cell(t, book.Result, 0, "body").(string); !strings.Contains(body,
		"20 ml lime cordial · 50 ml gin") || !strings.Contains(body, "Shake with ice") {
		t.Errorf("recipe body = %q, want the ordered ingredient line and the method", body)
	}

	// The nested editor: gin drags first, and the detail follows the new positions.
	recipeID := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM recipes", dbdomain.IntentQuery), 0, "id").(int64)
	ids := execSQL(t, deps, userID, bdbID,
		"SELECT id, item FROM recipe_ingredients ORDER BY position", dbdomain.IntentQuery)
	cordialID, ginID := cell(t, ids, 0, "id").(int64), cell(t, ids, 1, "id").(int64)
	runFormula(t, deps, userID, bdbID, "reorder-ingredients",
		map[string]any{"ordered_rowids": []any{float64(ginID), float64(cordialID)}})
	detail := runFormula(t, deps, userID, bdbID, "recipe-detail", map[string]any{"rowid": float64(recipeID)})
	if line, _ := cell(t, detail.Result, 0, "line").(string); line != "50 ml — gin" {
		t.Errorf("first ingredient after reorder = %q, want gin leading", line)
	}

	runFormula(t, deps, userID, bdbID, "log-from-recipe",
		map[string]any{"rowid": float64(recipeID), "rating": 4.5})
	made := execSQL(t, deps, userID, bdbID,
		"SELECT d.kind, t.rating FROM drinks d JOIN tastings t ON t.drink_id = d.id "+
			"WHERE d.name = 'Gimlet'", dbdomain.IntentQuery)
	if cell(t, made, 0, "kind") != "cocktail" || cell(t, made, 0, "rating") != int64(9) {
		t.Errorf("recipe log = %v, want the catalog drink created with its rated tasting", made.Rows)
	}
}
