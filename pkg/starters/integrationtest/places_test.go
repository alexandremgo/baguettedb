//go:build integration

package integrationtest

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
)

// applyPlaces applies the embedded places starter for a fresh user and returns the wired services with
// the new bdb's coordinates, ready for formula runs.
func applyPlaces(t *testing.T) (deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	userID = uuid.New()
	bdbID, err = svc.CreateFromStarter(t.Context(), userID, testRoles, "places", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter(places) error = %v", err)
	}
	return deps, userID, bdbID
}

// TestPlacesStatusOptionsAndFilters pins the single-view surface: the status enum narrows to each
// half and rejects off-vocabulary values naming the accepted ones, and the text search composes over
// name, city, address, and tags.
func TestPlacesStatusOptionsAndFilters(t *testing.T) {
	deps, userID, bdbID := applyPlaces(t)

	runFormula(t, deps, userID, bdbID, "add-place", map[string]any{
		"name": "Septime", "category": "restaurant", "city": "Paris", "tags": "date night", "visited": true,
	})
	runFormula(t, deps, userID, bdbID, "add-place",
		map[string]any{"name": "Bar Nouveau", "category": "bar", "city": "Paris"})

	if all := runFormula(t, deps, userID, bdbID, "places-cards", nil); len(all.Result.Rows) != 2 {
		t.Fatalf("unfiltered cards rows = %d, want 2", len(all.Result.Rows))
	}
	visited := runFormula(t, deps, userID, bdbID, "places-cards", map[string]any{"status": "visited"})
	if len(visited.Result.Rows) != 1 || cell(t, visited.Result, 0, "name") != "Septime" {
		t.Errorf("status=visited rows = %v, want only Septime", visited.Result.Rows)
	}
	wishlist := runFormula(t, deps, userID, bdbID, "places-cards", map[string]any{"status": "wishlist"})
	if len(wishlist.Result.Rows) != 1 || cell(t, wishlist.Result, 0, "name") != "Bar Nouveau" {
		t.Errorf("status=wishlist rows = %v, want only Bar Nouveau", wishlist.Result.Rows)
	}
	_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "places-cards",
		dbservice.FormulaRunInput{Params: map[string]any{"status": "pending"}})
	if !errors.Is(err, dbdomain.ErrFormulaParamValue) {
		t.Errorf("status=pending error = %v, want ErrFormulaParamValue", err)
	}

	if byTag := runFormula(t, deps, userID, bdbID, "places-cards",
		map[string]any{"search": "date night"}); len(byTag.Result.Rows) != 1 {
		t.Errorf("search='date night' rows = %d, want Septime via its tags", len(byTag.Result.Rows))
	}
	combined := runFormula(t, deps, userID, bdbID, "places-cards",
		map[string]any{"search": "paris", "status": "wishlist"})
	if len(combined.Result.Rows) != 1 || cell(t, combined.Result, 0, "name") != "Bar Nouveau" {
		t.Errorf("search+status rows = %v, want them composing to the bar", combined.Result.Rows)
	}
}

// TestPlacesCreatableCategory pins the pick-or-type category on both writers: the params are
// allow_new lookups over the catalog, a new spelling creates one row, a case-variant reuses it
// instead of splitting, the edit can mint a brand-new category, and a category-less edit clears it.
func TestPlacesCreatableCategory(t *testing.T) {
	deps, userID, bdbID := applyPlaces(t)

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	creatable := map[string]bool{"add-place": false, "edit-place": false}
	for _, f := range formulas {
		if _, ok := creatable[f.Name]; !ok {
			continue
		}
		for _, p := range f.Params {
			if p.Name == "category" {
				creatable[f.Name] = p.Lookup != nil && p.Lookup.AllowNew &&
					p.Lookup.Table == "categories" && p.Lookup.ValueColumn == "name"
			}
		}
	}
	for name, ok := range creatable {
		if !ok {
			t.Fatalf("%s category: want an allow_new lookup over categories.name", name)
		}
	}

	runFormula(t, deps, userID, bdbID, "add-place", map[string]any{"name": "Clamato", "category": "restaurant"})
	runFormula(t, deps, userID, bdbID, "add-place", map[string]any{"name": "Frenchie", "category": "Restaurant"})
	cats := execSQL(t, deps, userID, bdbID, "SELECT name FROM categories", dbdomain.IntentQuery)
	if len(cats.Rows) != 1 || cell(t, cats, 0, "name") != "restaurant" {
		t.Errorf("categories = %v, want the single original spelling", cats.Rows)
	}
	linked := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(DISTINCT category_id) AS n FROM places", dbdomain.IntentQuery)
	if v := cell(t, linked, 0, "n"); v != int64(1) {
		t.Errorf("distinct category_id across places = %v, want both resolving to the same row", v)
	}

	rowid := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM places WHERE name = 'Clamato'", dbdomain.IntentQuery), 0, "id")
	runFormula(t, deps, userID, bdbID, "edit-place",
		map[string]any{"rowid": float64(rowid.(int64)), "name": "Clamato", "category": "wine bar"})
	edited := execSQL(t, deps, userID, bdbID,
		"SELECT (SELECT name FROM categories WHERE id = p.category_id) AS category "+
			"FROM places p WHERE p.name = 'Clamato'", dbdomain.IntentQuery)
	if v := cell(t, edited, 0, "category"); v != "wine bar" {
		t.Errorf("category after the minting edit = %v, want the new catalog row", v)
	}
	runFormula(t, deps, userID, bdbID, "edit-place",
		map[string]any{"rowid": float64(rowid.(int64)), "name": "Clamato"})
	cleared := execSQL(t, deps, userID, bdbID,
		"SELECT (category_id IS NULL) AS no_cat FROM places WHERE name = 'Clamato'", dbdomain.IntentQuery)
	if v := cell(t, cleared, 0, "no_cat"); v != int64(1) {
		t.Errorf("category after a category-less edit = %v, want cleared — the edit rewrites whole", v)
	}
}

// TestPlacesQuickCaptureAndVisitFlip drives the consolidated loop: a name-only add (exactly what the
// quick bar sends — the sole required param is inline) lands as wishlist, and the toggle-visited row
// action flips it to visited and back.
func TestPlacesQuickCaptureAndVisitFlip(t *testing.T) {
	deps, userID, bdbID := applyPlaces(t)

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	for _, f := range formulas {
		if f.Name != "add-place" {
			continue
		}
		for _, p := range f.Params {
			if p.Name == "name" && !p.Inline {
				t.Error("add-place name: want inline so the quick bar renders")
			}
		}
	}

	runFormula(t, deps, userID, bdbID, "add-place", map[string]any{"name": "Le Verre Volé"})
	cards := runFormula(t, deps, userID, bdbID, "places-cards", map[string]any{"status": "wishlist"})
	if len(cards.Result.Rows) != 1 {
		t.Fatalf("wishlist-status rows after quick capture = %d, want 1", len(cards.Result.Rows))
	}
	rowid := cell(t, cards.Result, 0, "rowid").(int64)

	runFormula(t, deps, userID, bdbID, "mark-visited", map[string]any{"rowid": float64(rowid)})
	if v := runFormula(t, deps, userID, bdbID, "places-cards",
		map[string]any{"status": "visited"}); len(v.Result.Rows) != 1 {
		t.Fatalf("visited-status rows after the flip = %d, want 1", len(v.Result.Rows))
	}

	runFormula(t, deps, userID, bdbID, "mark-visited", map[string]any{"rowid": float64(rowid)})
	if w := runFormula(t, deps, userID, bdbID, "places-cards",
		map[string]any{"status": "wishlist"}); len(w.Result.Rows) != 1 {
		t.Errorf("wishlist-status rows after the flip back = %d, want 1", len(w.Result.Rows))
	}
}

// TestPlacesEditVisitedDate pins edit-place's authority over the visit date: a supplied visited_on
// backdates the visit (flipping the status), the cards view exposes it as the seed column, and an
// edit clearing it moves the place back to the wishlist — the formula rewrites the place whole.
func TestPlacesEditVisitedDate(t *testing.T) {
	deps, userID, bdbID := applyPlaces(t)

	runFormula(t, deps, userID, bdbID, "add-place", map[string]any{"name": "Le Baratin"})
	rowid := cell(t, execSQL(t, deps, userID, bdbID, "SELECT id FROM places", dbdomain.IntentQuery), 0, "id")

	runFormula(t, deps, userID, bdbID, "edit-place",
		map[string]any{"rowid": float64(rowid.(int64)), "name": "Le Baratin", "visited_on": "2026-07-14"})
	cards := runFormula(t, deps, userID, bdbID, "places-cards", map[string]any{"status": "visited"})
	if len(cards.Result.Rows) != 1 {
		t.Fatalf("visited rows after the backdated edit = %d, want 1", len(cards.Result.Rows))
	}
	if v := cell(t, cards.Result, 0, "visited_on"); v != "2026-07-14" {
		t.Errorf("visited_on seed column = %v, want the supplied date", v)
	}

	runFormula(t, deps, userID, bdbID, "edit-place",
		map[string]any{"rowid": float64(rowid.(int64)), "name": "Le Baratin"})
	back := runFormula(t, deps, userID, bdbID, "places-cards", map[string]any{"status": "wishlist"})
	if len(back.Result.Rows) != 1 {
		t.Errorf("wishlist rows after a date-less edit = %d, want the place moved back", len(back.Result.Rows))
	}
}

// TestPlacesLinkRendering pins the link labeling: no custom link and maps-looking links keep the
// single "Open in Maps" (pointing at the custom pin when set), while a website link renders as
// "Open link" alongside the always-computed Maps search; the detail keeps the two as separate
// columns so neither is mislabeled.
func TestPlacesLinkRendering(t *testing.T) {
	deps, userID, bdbID := applyPlaces(t)

	runFormula(t, deps, userID, bdbID, "add-place", map[string]any{"name": "Le Baratin"})
	runFormula(t, deps, userID, bdbID, "add-place",
		map[string]any{"name": "Septime", "link": "https://maps.app.goo.gl/abc123"})
	runFormula(t, deps, userID, bdbID, "add-place",
		map[string]any{"name": "Clamato", "link": "https://clamato-charonne.fr"})

	cards := runFormula(t, deps, userID, bdbID, "places-cards", nil)
	byName := map[string]string{}
	for i := range cards.Result.Rows {
		body, _ := cell(t, cards.Result, i, "body").(string)
		byName[cell(t, cards.Result, i, "name").(string)] = body
	}
	if body := byName["Le Baratin"]; !strings.Contains(body,
		"[Open in Maps ↗](https://www.google.com/maps/search/") {
		t.Errorf("no-link body = %q, want the computed Maps link", body)
	}
	if body := byName["Septime"]; !strings.Contains(body, "[Open in Maps ↗](https://maps.app.goo.gl/abc123)") {
		t.Errorf("maps-link body = %q, want the single Maps label on the custom pin", body)
	}
	body := byName["Clamato"]
	if !strings.Contains(body, "[Open link ↗](https://clamato-charonne.fr)") ||
		!strings.Contains(body, "[Maps ↗](https://www.google.com/maps/search/") {
		t.Errorf("website-link body = %q, want Open link plus the computed Maps link", body)
	}

	rowid := cell(t, execSQL(t, deps, userID, bdbID,
		"SELECT id FROM places WHERE name = 'Clamato'", dbdomain.IntentQuery), 0, "id")
	detail := runFormula(t, deps, userID, bdbID, "place-detail", map[string]any{"rowid": float64(rowid.(int64))})
	if v := cell(t, detail.Result, 0, "link"); v != "https://clamato-charonne.fr" {
		t.Errorf("detail link = %v, want the raw custom URL", v)
	}
	if v, _ := cell(t, detail.Result, 0, "maps").(string); !strings.Contains(v, "google.com/maps/search") {
		t.Errorf("detail maps = %q, want the computed search regardless of the custom link", v)
	}
}
