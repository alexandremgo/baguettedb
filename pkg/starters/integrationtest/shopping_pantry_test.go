//go:build integration

package integrationtest

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
)

// applyShoppingPantry applies the embedded shopping-pantry starter for a fresh user and returns the wired
// services with the new bdb's coordinates, ready for formula runs.
func applyShoppingPantry(t *testing.T) (deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	userID = uuid.New()
	bdbID, err = svc.CreateFromStarter(t.Context(), userID, testRoles, "shopping-pantry", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter(shopping-pantry) error = %v", err)
	}
	return deps, userID, bdbID
}

// TestGroceriesCatalogIsCaseInsensitive drives the catalog identity rule through real formula runs: adding
// the same item under a different case reuses the one catalog row instead of splitting it in two, and the
// second line still resolves to an item — a name lookup that missed would insert nothing at all.
func TestGroceriesCatalogIsCaseInsensitive(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "Milk"})
	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk"})

	items := execSQL(t, deps, userID, bdbID, "SELECT id, name FROM items", dbdomain.IntentQuery)
	if len(items.Rows) != 1 {
		t.Fatalf("items = %v, want the one catalog row", items.Rows)
	}
	if name := cell(t, items, 0, "name"); name != "Milk" {
		t.Errorf("catalog name = %v, want the first spelling kept", name)
	}

	lines := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM groceries WHERE item_id = (SELECT id FROM items)", dbdomain.IntentQuery)
	if n := cell(t, lines, 0, "n"); n != int64(2) {
		t.Errorf("lines on the catalog item = %v, want 2", n)
	}
}

// TestGroceriesQuickAddWritesTheWholeLine drives the quick-add's optional fields end to end: quantity,
// unit and store land on the grocery row, the rendered line shows a whole quantity without the REAL's
// trailing decimal, and a shop already on file keeps its spelling when typed in another case.
func TestGroceriesQuickAddWritesTheWholeLine(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery",
		map[string]any{"item": "milk", "quantity": 2.0, "store": "Green Market"})

	list := runFormula(t, deps, userID, bdbID, "pending-groceries", nil)
	if line := cell(t, list.Result, 0, "line"); line != "milk — 2 @ Green Market" {
		t.Errorf("line = %q, want a whole quantity rendered without its decimal", line)
	}

	runFormula(t, deps, userID, bdbID, "add-grocery",
		map[string]any{"item": "butter", "quantity": 0.5, "unit": "kg", "store": "GREEN MARKET"})

	rows := execSQL(t, deps, userID, bdbID,
		"SELECT quantity, unit, store FROM groceries ORDER BY id DESC LIMIT 1", dbdomain.IntentQuery)
	if q := cell(t, rows, 0, "quantity"); q != 0.5 {
		t.Errorf("quantity = %v, want 0.5", q)
	}
	if u := cell(t, rows, 0, "unit"); u != "kg" {
		t.Errorf("unit = %v, want kg", u)
	}
	if s := cell(t, rows, 0, "store"); s != "Green Market" {
		t.Errorf("store = %v, want the spelling already on file", s)
	}
}

// TestGroceriesQuickAddIgnoresABlankItem pins the empty-submit guard: with nothing typed, neither the
// catalog nor the list gains a row — an unguarded insert would create an item named "".
func TestGroceriesQuickAddIgnoresABlankItem(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "   "})

	counts := execSQL(t, deps, userID, bdbID,
		"SELECT (SELECT COUNT(*) FROM items) AS items, (SELECT COUNT(*) FROM groceries) AS lines",
		dbdomain.IntentQuery)
	if n := cell(t, counts, 0, "items"); n != int64(0) {
		t.Errorf("items = %v, want none", n)
	}
	if n := cell(t, counts, 0, "lines"); n != int64(0) {
		t.Errorf("lines = %v, want none", n)
	}
}

// TestGroceriesFilterByStore pins the shopping list's store filter: an unset param leaves every line,
// and a store narrows to it whatever case is bound.
func TestGroceriesFilterByStore(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk", "store": "Green Market"})
	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "nails", "store": "Corner Shop"})

	if all := runFormula(t, deps, userID, bdbID, "pending-groceries", nil); len(all.Result.Rows) != 2 {
		t.Errorf("unfiltered rows = %d, want 2", len(all.Result.Rows))
	}
	narrowed := runFormula(t, deps, userID, bdbID, "pending-groceries", map[string]any{"store": "green market"})
	if len(narrowed.Result.Rows) != 1 {
		t.Fatalf("filtered rows = %d, want 1", len(narrowed.Result.Rows))
	}
	if line := cell(t, narrowed.Result, 0, "line"); line != "milk @ Green Market" {
		t.Errorf("filtered line = %q, want the Green Market one", line)
	}
}

// TestGroceriesEditItemRenamesAndCreatesCategory drives the catalog edit: a typed name renames the item
// everywhere it is used, a category typed past the picker's options is created and assigned, and an empty
// name leaves the current one alone rather than blanking it — while an omitted category clears it.
func TestGroceriesEditItemRenamesAndCreatesCategory(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk"})
	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk"})
	added := execSQL(t, deps, userID, bdbID, "SELECT MIN(id) AS id FROM groceries", dbdomain.IntentQuery)
	rowid := float64(cell(t, added, 0, "id").(int64))

	runFormula(t, deps, userID, bdbID, "edit-item",
		map[string]any{"rowid": rowid, "name": "whole milk", "category": "Dairy"})

	item := execSQL(t, deps, userID, bdbID,
		"SELECT i.name, c.name AS category FROM items i LEFT JOIN categories c ON c.id = i.category_id",
		dbdomain.IntentQuery)
	if len(item.Rows) != 1 {
		t.Fatalf("items = %v, want the one renamed row — a rename must not fork the catalog", item.Rows)
	}
	if name := cell(t, item, 0, "name"); name != "whole milk" {
		t.Errorf("name = %v, want the rename applied", name)
	}
	if category := cell(t, item, 0, "category"); category != "Dairy" {
		t.Errorf("category = %v, want the free-text one created and assigned", category)
	}
	// Both lines follow the rename, since they share the catalog row.
	list := runFormula(t, deps, userID, bdbID, "pending-groceries", nil)
	for i := range list.Result.Rows {
		if line := cell(t, list.Result, i, "line"); line != "whole milk" {
			t.Errorf("line %d = %q, want the renamed item", i, line)
		}
	}

	runFormula(t, deps, userID, bdbID, "edit-item", map[string]any{"rowid": rowid, "notes": "the blue one"})
	item = execSQL(t, deps, userID, bdbID, "SELECT name, notes, category_id FROM items", dbdomain.IntentQuery)
	if name := cell(t, item, 0, "name"); name != "whole milk" {
		t.Errorf("name = %v after an empty rename, want it kept", name)
	}
	if notes := cell(t, item, 0, "notes"); notes != "the blue one" {
		t.Errorf("notes = %v, want the edit applied", notes)
	}
	if category := cell(t, item, 0, "category_id"); category != nil {
		t.Errorf("category_id = %v after an omitted category, want the assignment cleared", category)
	}
}

// TestPantryExpiringFilter pins the expiring-within-days filter: it keeps only items whose soonest batch
// expires inside the window, 0 means today-or-already-expired, and the expired item leads the order. An
// unset filter changes nothing.
func TestPantryExpiringFilter(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	today := time.Now()
	buyItem(t, deps, userID, bdbID, "old-yoghurt", "2020-01-01")
	buyItem(t, deps, userID, bdbID, "milk", today.AddDate(0, 0, 3).Format(time.DateOnly))
	buyItem(t, deps, userID, bdbID, "flour", today.AddDate(0, 0, 30).Format(time.DateOnly))
	buyItem(t, deps, userID, bdbID, "salt", "")

	week := runFormula(t, deps, userID, bdbID, "pantry", map[string]any{"expiring_within_days": 7.0})
	order := make([]string, 0, len(week.Result.Rows))
	for i := range week.Result.Rows {
		name, _ := cell(t, week.Result, i, "name").(string)
		order = append(order, name)
	}
	if !slices.Equal(order, []string{"old-yoghurt", "milk"}) {
		t.Errorf("within-7-days = %v, want the expired one leading and the +3-days one after", order)
	}

	urgent := runFormula(t, deps, userID, bdbID, "pantry", map[string]any{"expiring_within_days": 0.0})
	if len(urgent.Result.Rows) != 1 {
		t.Fatalf("within-0-days rows = %d, want only the already-expired one", len(urgent.Result.Rows))
	}
	if name := cell(t, urgent.Result, 0, "name"); name != "old-yoghurt" {
		t.Errorf("within-0-days name = %v, want old-yoghurt", name)
	}

	if all := runFormula(t, deps, userID, bdbID, "pantry", nil); len(all.Result.Rows) != 4 {
		t.Errorf("unfiltered rows = %d, want all 4 in stock — an unset filter must not narrow", len(all.Result.Rows))
	}
}

// TestItemBatchesLineSurvivesABareBatch pins the batch detail's title against NULL propagation: a batch
// stocked from a line with no quantity, unit, or expiry has only its acquisition date to show, and the
// title must show it rather than concatenate itself to NULL and render a blank row.
func TestItemBatchesLineSurvivesABareBatch(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "saffron"})
	added := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM groceries", dbdomain.IntentQuery)
	runFormula(t, deps, userID, bdbID, "mark-bought",
		map[string]any{"rowid": float64(cell(t, added, 0, "id").(int64))})

	items := execSQL(t, deps, userID, bdbID, "SELECT id FROM items WHERE name = 'saffron'", dbdomain.IntentQuery)
	batches := runFormula(t, deps, userID, bdbID, "item-batches",
		map[string]any{"rowid": float64(cell(t, items, 0, "id").(int64))})
	if len(batches.Result.Rows) != 1 {
		t.Fatalf("batch rows = %d, want the one stocked batch", len(batches.Result.Rows))
	}
	line, _ := cell(t, batches.Result, 0, "line").(string)
	if !strings.HasPrefix(line, "added ") {
		t.Errorf("bare-batch line = %q, want it anchored on the acquisition date", line)
	}
}

// buyItem puts an item on the list and checks it off, which stocks the pantry, returning the catalog
// item's id. An expiry is set afterwards from the batch, the way the app does it.
func buyItem(t *testing.T, deps engineDeps, userID, bdbID uuid.UUID, item, expires string) float64 {
	t.Helper()
	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": item, "quantity": 2.0})
	added := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM groceries", dbdomain.IntentQuery)
	runFormula(t, deps, userID, bdbID, "mark-bought",
		map[string]any{"rowid": float64(cell(t, added, 0, "id").(int64))})

	if expires != "" {
		batch := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM inventory", dbdomain.IntentQuery)
		runFormula(t, deps, userID, bdbID, "edit-batch", map[string]any{
			"rowid": float64(cell(t, batch, 0, "id").(int64)), "quantity": 2.0, "expires_at": expires,
		})
	}

	ids := execSQL(t, deps, userID, bdbID, "SELECT id FROM items WHERE name = '"+item+"'", dbdomain.IntentQuery)
	return float64(cell(t, ids, 0, "id").(int64))
}

// TestGroceriesCheckingOffStocksThePantry drives the one-tap buy: checking a line marks it bought and
// puts a batch in the pantry, and un-checking takes that exact batch back out.
func TestGroceriesCheckingOffStocksThePantry(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery",
		map[string]any{"item": "milk", "quantity": 2.0, "unit": "l"})
	added := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM groceries", dbdomain.IntentQuery)
	rowid := float64(cell(t, added, 0, "id").(int64))

	runFormula(t, deps, userID, bdbID, "mark-bought", map[string]any{"rowid": rowid})
	batch := execSQL(t, deps, userID, bdbID, "SELECT quantity, unit, expires_at FROM inventory",
		dbdomain.IntentQuery)
	if len(batch.Rows) != 1 {
		t.Fatalf("batches = %v, want the check to have stocked one", batch.Rows)
	}
	if q := cell(t, batch, 0, "quantity"); q != 2.0 {
		t.Errorf("batch quantity = %v, want the line's 2", q)
	}
	if u := cell(t, batch, 0, "unit"); u != "l" {
		t.Errorf("batch unit = %v, want the line's l", u)
	}
	if e := cell(t, batch, 0, "expires_at"); e != nil {
		t.Errorf("batch expiry = %v, want none — it is set from the pantry", e)
	}

	runFormula(t, deps, userID, bdbID, "mark-bought", map[string]any{"rowid": rowid})
	after := execSQL(t, deps, userID, bdbID,
		"SELECT (SELECT COUNT(*) FROM inventory) AS batches,"+
			" (SELECT COUNT(*) FROM groceries WHERE bought_at IS NULL) AS pending", dbdomain.IntentQuery)
	if n := cell(t, after, 0, "batches"); n != int64(0) {
		t.Errorf("batches after un-checking = %v, want the one the check made gone", n)
	}
	if n := cell(t, after, 0, "pending"); n != int64(1) {
		t.Errorf("pending lines = %v, want the line back on the list", n)
	}
}

// TestGroceriesUncheckingKeepsATouchedBatch pins the un-tap guard: a batch given an expiry since the
// check is a correction, so un-checking gives the line back without discarding it.
func TestGroceriesUncheckingKeepsATouchedBatch(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk", "quantity": 2.0})
	added := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM groceries", dbdomain.IntentQuery)
	rowid := float64(cell(t, added, 0, "id").(int64))
	runFormula(t, deps, userID, bdbID, "mark-bought", map[string]any{"rowid": rowid})

	batch := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM inventory", dbdomain.IntentQuery)
	runFormula(t, deps, userID, bdbID, "edit-batch", map[string]any{
		"rowid": float64(cell(t, batch, 0, "id").(int64)), "quantity": 2.0, "expires_at": "2026-09-30",
	})

	runFormula(t, deps, userID, bdbID, "mark-bought", map[string]any{"rowid": rowid})
	kept := execSQL(t, deps, userID, bdbID, "SELECT expires_at FROM inventory", dbdomain.IntentQuery)
	if len(kept.Rows) != 1 {
		t.Fatalf("batches = %v, want the edited one kept", kept.Rows)
	}
	if e := cell(t, kept, 0, "expires_at"); e != "2026-09-30" {
		t.Errorf("expiry = %v, want the correction intact", e)
	}
}

// TestPantryHidesWhatIsNotInStock pins the pantry's default filter and its ordering: only items with a
// batch show until show_all is set, and the soonest expiry leads.
func TestPantryHidesWhatIsNotInStock(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	// Names chosen so alphabetical order and expiry order disagree: sorting by name would lead with
	// apples, so only an expiry-aware ORDER BY puts milk first.
	buyItem(t, deps, userID, bdbID, "yoghurt", "")
	buyItem(t, deps, userID, bdbID, "apples", "2026-12-01")
	buyItem(t, deps, userID, bdbID, "milk", "2026-08-12")
	// On the list but never bought, so it has no batch.
	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "spinach"})

	inStock := runFormula(t, deps, userID, bdbID, "pantry", nil)
	if len(inStock.Result.Rows) != 3 {
		t.Fatalf("in-stock rows = %d, want 3 — spinach has no batch", len(inStock.Result.Rows))
	}
	order := make([]string, 0, len(inStock.Result.Rows))
	for i := range inStock.Result.Rows {
		name, _ := cell(t, inStock.Result, i, "name").(string)
		order = append(order, name)
	}
	if !slices.Equal(order, []string{"milk", "apples", "yoghurt"}) {
		t.Errorf("pantry order = %v, want soonest expiry first and no-expiry last", order)
	}
	if n := cell(t, inStock.Result, 0, "times_bought"); n != int64(1) {
		t.Errorf("times_bought = %v, want 1", n)
	}

	all := runFormula(t, deps, userID, bdbID, "pantry", map[string]any{"show_all": true})
	if len(all.Result.Rows) != 4 {
		t.Errorf("show_all rows = %d, want 4 — the whole catalog", len(all.Result.Rows))
	}
}

// TestPantryQueryFilter pins the pantry's search: one box matches the item name and its category alike,
// an item without a category is searchable by name, and an unset query narrows nothing.
func TestPantryQueryFilter(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)
	milk := buyItem(t, deps, userID, bdbID, "milk", "")
	buyItem(t, deps, userID, bdbID, "bread", "")

	runFormula(t, deps, userID, bdbID, "edit-catalog-item", map[string]any{
		"rowid": milk, "category": "dairy",
	})

	byName := runFormula(t, deps, userID, bdbID, "pantry", map[string]any{"query": "brea"})
	if len(byName.Result.Rows) != 1 || cell(t, byName.Result, 0, "name") != "bread" {
		t.Fatalf("name-query rows = %v, want the categoryless bread alone", byName.Result.Rows)
	}
	byCategory := runFormula(t, deps, userID, bdbID, "pantry", map[string]any{"query": "dairy"})
	if len(byCategory.Result.Rows) != 1 || cell(t, byCategory.Result, 0, "name") != "milk" {
		t.Fatalf("category-query rows = %v, want milk found through its category", byCategory.Result.Rows)
	}
	if all := runFormula(t, deps, userID, bdbID, "pantry", nil); len(all.Result.Rows) != 2 {
		t.Errorf("unfiltered rows = %d, want 2 — an unset query must not narrow", len(all.Result.Rows))
	}
}

// TestPantryUseSomeEmptiesTheBatch drives the batch detail's decrement: an amount comes off the batch,
// and the batch disappears once nothing is left rather than lingering at zero.
func TestPantryUseSomeEmptiesTheBatch(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)
	itemID := buyItem(t, deps, userID, bdbID, "milk", "")

	batches := runFormula(t, deps, userID, bdbID, "item-batches", map[string]any{"rowid": itemID})
	batchID := float64(cell(t, batches.Result, 0, "rowid").(int64))
	if line := cell(t, batches.Result, 0, "line"); !strings.HasPrefix(line.(string), "2") {
		t.Errorf("batch line = %q, want it to start with the quantity", line)
	}

	runFormula(t, deps, userID, bdbID, "use-some", map[string]any{"rowid": batchID, "amount": 0.5})
	left := execSQL(t, deps, userID, bdbID, "SELECT quantity FROM inventory", dbdomain.IntentQuery)
	if q := cell(t, left, 0, "quantity"); q != 1.5 {
		t.Errorf("quantity = %v, want 1.5", q)
	}

	runFormula(t, deps, userID, bdbID, "use-some", map[string]any{"rowid": batchID, "amount": 1.5})
	empty := execSQL(t, deps, userID, bdbID, "SELECT COUNT(*) AS n FROM inventory", dbdomain.IntentQuery)
	if n := cell(t, empty, 0, "n"); n != int64(0) {
		t.Errorf("batches = %v, want the emptied one gone", n)
	}
}

// TestPantryMergeItemAbsorbsTheDuplicate drives the duplicate cleanup: the tapped row survives, the
// picked duplicate's lines and batches move onto it, and the duplicate leaves the catalog.
func TestPantryMergeItemAbsorbsTheDuplicate(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)
	keep := buyItem(t, deps, userID, bdbID, "spinach", "")
	duplicate := buyItem(t, deps, userID, bdbID, "epinard", "")

	runFormula(t, deps, userID, bdbID, "merge-item", map[string]any{"rowid": keep, "duplicate_id": duplicate})

	items := execSQL(t, deps, userID, bdbID, "SELECT name FROM items", dbdomain.IntentQuery)
	if len(items.Rows) != 1 || cell(t, items, 0, "name") != "spinach" {
		t.Fatalf("items = %v, want only the surviving spinach", items.Rows)
	}
	moved := execSQL(t, deps, userID, bdbID,
		"SELECT (SELECT COUNT(*) FROM groceries WHERE item_id = i.id) AS lines,"+
			" (SELECT COUNT(*) FROM inventory WHERE item_id = i.id) AS batches FROM items i",
		dbdomain.IntentQuery)
	if n := cell(t, moved, 0, "lines"); n != int64(2) {
		t.Errorf("lines on the survivor = %v, want both", n)
	}
	if n := cell(t, moved, 0, "batches"); n != int64(2) {
		t.Errorf("batches on the survivor = %v, want both", n)
	}
}

// TestPantryMergeItemIntoItselfChangesNothing pins the self-merge guard: picking the tapped row as its
// own duplicate must not delete the item out from under its lines and batches.
func TestPantryMergeItemIntoItselfChangesNothing(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)
	itemID := buyItem(t, deps, userID, bdbID, "milk", "")

	runFormula(t, deps, userID, bdbID, "merge-item", map[string]any{"rowid": itemID, "duplicate_id": itemID})

	counts := execSQL(t, deps, userID, bdbID,
		"SELECT (SELECT COUNT(*) FROM items) AS items, (SELECT COUNT(*) FROM inventory) AS batches",
		dbdomain.IntentQuery)
	if n := cell(t, counts, 0, "items"); n != int64(1) {
		t.Errorf("items = %v, want the item kept", n)
	}
	if n := cell(t, counts, 0, "batches"); n != int64(1) {
		t.Errorf("batches = %v, want the batch kept", n)
	}
}

// TestPantryDeleteCatalogItemKeepsReferencedOnes pins the prune path: a referenced item is refused by the
// foreign key, an orphan goes. That refusal is the feature — it is what makes deleting safe to offer.
func TestPantryDeleteCatalogItemKeepsReferencedOnes(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)
	referenced := buyItem(t, deps, userID, bdbID, "milk", "")

	_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "delete-catalog-item",
		dbservice.FormulaRunInput{Params: map[string]any{"rowid": referenced}})
	if err == nil {
		t.Fatal("delete-catalog-item on a referenced item succeeded, want the foreign key to refuse it")
	}

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "nails"})
	execSQL(t, deps, userID, bdbID, "DELETE FROM groceries WHERE item_id = (SELECT id FROM items"+
		" WHERE name = 'nails')", dbdomain.IntentExecute)
	orphan := execSQL(t, deps, userID, bdbID, "SELECT id FROM items WHERE name = 'nails'", dbdomain.IntentQuery)
	runFormula(t, deps, userID, bdbID, "delete-catalog-item",
		map[string]any{"rowid": float64(cell(t, orphan, 0, "id").(int64))})

	left := execSQL(t, deps, userID, bdbID, "SELECT name FROM items", dbdomain.IntentQuery)
	if len(left.Rows) != 1 || cell(t, left, 0, "name") != "milk" {
		t.Errorf("items = %v, want only the referenced milk left", left.Rows)
	}
}

// TestGroceriesEditLineCanonicalisesTheStore pins the edit dialog's store write: a shop already on file
// is stored in the spelling the picker offers whatever case is typed, a shop nobody has used yet keeps
// what was typed, and a cleared field stores NULL rather than ” so it never shows up as a blank option.
func TestGroceriesEditLineCanonicalisesTheStore(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk", "store": "Green Market"})
	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "nails"})
	added := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM groceries", dbdomain.IntentQuery)
	rowid := float64(cell(t, added, 0, "id").(int64))

	storeOf := func() any {
		t.Helper()
		row := execSQL(t, deps, userID, bdbID,
			"SELECT store FROM groceries WHERE id = "+strconv.Itoa(int(rowid)), dbdomain.IntentQuery)
		return cell(t, row, 0, "store")
	}

	runFormula(t, deps, userID, bdbID, "edit-grocery", map[string]any{"rowid": rowid, "store": " green market "})
	if s := storeOf(); s != "Green Market" {
		t.Errorf("store = %v, want the spelling already on file", s)
	}
	runFormula(t, deps, userID, bdbID, "edit-grocery", map[string]any{"rowid": rowid, "store": " Corner Shop "})
	if s := storeOf(); s != "Corner Shop" {
		t.Errorf("store = %v, want a new shop kept as typed, trimmed", s)
	}
	runFormula(t, deps, userID, bdbID, "edit-grocery", map[string]any{"rowid": rowid, "store": "  "})
	if s := storeOf(); s != nil {
		t.Errorf("store = %v, want NULL for a cleared field", s)
	}
	options := execSQL(t, deps, userID, bdbID, "SELECT store FROM store_options", dbdomain.IntentQuery)
	if len(options.Rows) != 1 || cell(t, options, 0, "store") != "Green Market" {
		t.Errorf("store options = %v, want only the shop still in use", options.Rows)
	}
}

// TestGroceriesStoreOptionsFoldCase pins the store filter's options source: shops written in different
// cases collapse to one entry, spelled the way it was written most often.
func TestGroceriesStoreOptionsFoldCase(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	// Written straight to the table: every formula that sets a store canonicalises it against the view
	// first, so the raw case variants the view must fold never reach it through a formula.
	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk"})
	for _, store := range []string{"green market", "green market", "Green Market", "Corner Shop"} {
		execSQL(t, deps, userID, bdbID,
			"INSERT INTO groceries (item_id, store) SELECT id, '"+store+"' FROM items WHERE name = 'milk'",
			dbdomain.IntentExecute)
	}

	// The view carries no ORDER BY — the picker sorts its own options — so compare as a set.
	options := execSQL(t, deps, userID, bdbID, "SELECT store FROM store_options", dbdomain.IntentQuery)
	stores := make([]string, 0, len(options.Rows))
	for i := range options.Rows {
		store, _ := cell(t, options, i, "store").(string)
		stores = append(stores, store)
	}
	slices.Sort(stores)
	if !slices.Equal(stores, []string{"Corner Shop", "green market"}) {
		t.Errorf("store options = %v, want one entry per shop, each in its most-used spelling", stores)
	}
}

// TestPantryAddToListSkipsAPendingLine drives the pantry's restock tap: it puts the item back on the list
// bare, does nothing while that line is still pending, and works again once the line has been bought.
func TestPantryAddToListSkipsAPendingLine(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)
	itemID := buyItem(t, deps, userID, bdbID, "milk", "")

	runFormula(t, deps, userID, bdbID, "add-to-list", map[string]any{"rowid": itemID})
	runFormula(t, deps, userID, bdbID, "add-to-list", map[string]any{"rowid": itemID})

	pending := execSQL(t, deps, userID, bdbID,
		"SELECT quantity, unit, store FROM groceries WHERE bought_at IS NULL", dbdomain.IntentQuery)
	if len(pending.Rows) != 1 {
		t.Fatalf("pending lines = %d, want the second tap to have skipped", len(pending.Rows))
	}
	for _, col := range []string{"quantity", "unit", "store"} {
		if v := cell(t, pending, 0, col); v != nil {
			t.Errorf("restocked line %s = %v, want the bare line — details get edited on the list", col, v)
		}
	}

	// Once bought, the pending guard no longer sees the line, so the next restock inserts again.
	added := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM groceries", dbdomain.IntentQuery)
	runFormula(t, deps, userID, bdbID, "mark-bought",
		map[string]any{"rowid": float64(cell(t, added, 0, "id").(int64))})
	runFormula(t, deps, userID, bdbID, "add-to-list", map[string]any{"rowid": itemID})
	counts := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM groceries WHERE bought_at IS NULL", dbdomain.IntentQuery)
	if n := cell(t, counts, 0, "n"); n != int64(1) {
		t.Errorf("pending lines after re-buying = %v, want the item back on the list once", n)
	}
}

// TestPantryStockItemStocksOffTheList drives the pantry's add dialog: a batch lands with its quantity,
// unit and expiry in one go, a different case reuses the catalog entry rather than splitting it, and a
// blank name inserts nothing at all.
func TestPantryStockItemStocksOffTheList(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "stock-item",
		map[string]any{"item": "Flour", "quantity": 1.0, "unit": "kg", "expires_at": "2026-12-01"})
	batch := execSQL(t, deps, userID, bdbID,
		"SELECT quantity, unit, expires_at FROM inventory", dbdomain.IntentQuery)
	if len(batch.Rows) != 1 {
		t.Fatalf("batches = %v, want the one stocked batch", batch.Rows)
	}
	if q := cell(t, batch, 0, "quantity"); q != 1.0 {
		t.Errorf("batch quantity = %v, want 1", q)
	}
	if u := cell(t, batch, 0, "unit"); u != "kg" {
		t.Errorf("batch unit = %v, want kg", u)
	}
	if e := cell(t, batch, 0, "expires_at"); e != "2026-12-01" {
		t.Errorf("batch expiry = %v, want the dialog's date — unlike a checked-off buy, it is set here", e)
	}

	runFormula(t, deps, userID, bdbID, "stock-item", map[string]any{"item": "flour"})
	runFormula(t, deps, userID, bdbID, "stock-item", map[string]any{"item": "   "})
	counts := execSQL(t, deps, userID, bdbID,
		"SELECT (SELECT COUNT(*) FROM items) AS items, (SELECT COUNT(*) FROM inventory) AS batches",
		dbdomain.IntentQuery)
	if n := cell(t, counts, 0, "items"); n != int64(1) {
		t.Errorf("items = %v, want the one catalog row whatever the case, and none for a blank name", n)
	}
	if n := cell(t, counts, 0, "batches"); n != int64(2) {
		t.Errorf("batches = %v, want the bare restock added and the blank one refused", n)
	}
}

// TestGroceriesAtHomeDetail drives the shopping-list row detail: a fresh item shows nothing, batches read
// with the pantry's line formatting soonest expiry first, and the purchase history closes the list.
func TestGroceriesAtHomeDetail(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)
	today := time.Now().UTC().Format(time.DateOnly)

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk"})
	added := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM groceries", dbdomain.IntentQuery)
	lineID := float64(cell(t, added, 0, "id").(int64))

	if home := runFormula(t, deps, userID, bdbID, "grocery-at-home",
		map[string]any{"rowid": lineID}); len(home.Result.Rows) != 0 {
		t.Fatalf("fresh-item rows = %d, want nothing at home and no history line", len(home.Result.Rows))
	}

	runFormula(t, deps, userID, bdbID, "stock-item",
		map[string]any{"item": "milk", "quantity": 2.0, "unit": "l", "expires_at": "2026-09-01"})
	runFormula(t, deps, userID, bdbID, "stock-item", map[string]any{"item": "milk"})
	home := runFormula(t, deps, userID, bdbID, "grocery-at-home", map[string]any{"rowid": lineID})
	if len(home.Result.Rows) != 2 {
		t.Fatalf("stocked rows = %d, want both batches and still no history", len(home.Result.Rows))
	}
	if line := cell(t, home.Result, 0, "line"); line != "2 l — expires 2026-09-01 — added "+today {
		t.Errorf("dated batch line = %q, want it leading with the pantry's formatting", line)
	}
	if line := cell(t, home.Result, 1, "line"); line != "added "+today {
		t.Errorf("bare batch line = %q, want the no-expiry stock after the dated batch", line)
	}

	// Buying the line stocks a third (bare) batch and starts the history trailer.
	runFormula(t, deps, userID, bdbID, "mark-bought", map[string]any{"rowid": lineID})
	home = runFormula(t, deps, userID, bdbID, "grocery-at-home", map[string]any{"rowid": lineID})
	if len(home.Result.Rows) != 4 {
		t.Fatalf("bought rows = %d, want three batches and the history line", len(home.Result.Rows))
	}
	if line := cell(t, home.Result, 3, "line"); line != "bought once — "+today {
		t.Errorf("history line = %q, want the singular form closing the list", line)
	}

	// A second purchase pluralizes it.
	items := execSQL(t, deps, userID, bdbID, "SELECT id FROM items WHERE name = 'milk'", dbdomain.IntentQuery)
	runFormula(t, deps, userID, bdbID, "add-to-list",
		map[string]any{"rowid": float64(cell(t, items, 0, "id").(int64))})
	restocked := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM groceries", dbdomain.IntentQuery)
	runFormula(t, deps, userID, bdbID, "mark-bought",
		map[string]any{"rowid": float64(cell(t, restocked, 0, "id").(int64))})
	home = runFormula(t, deps, userID, bdbID, "grocery-at-home", map[string]any{"rowid": lineID})
	if line := cell(t, home.Result, len(home.Result.Rows)-1, "line"); line != "bought 2 times — last "+today {
		t.Errorf("history line = %q, want the plural count with its last date", line)
	}
}

// TestGroceriesDisplayWiring pins the slot wiring the starter adds beyond the defaults: the shopping list
// drills into the at-home detail, the pantry's add is the stock dialog rather than an inline bar, and the
// everyday restock action leads its row menu ahead of the rare merge.
func TestGroceriesDisplayWiring(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	byName := make(map[string]*dbdomain.Formula, len(formulas))
	for _, f := range formulas {
		byName[f.Name] = f
	}

	list := byName["pending-groceries"]
	if list.Display == nil || list.Display.Slots.Detail != byName["grocery-at-home"].ID.String() {
		t.Fatalf("pending-groceries detail slot = %+v, want grocery-at-home", list.Display)
	}
	// The edit slot's own formula leads the row menu so the two edits sit together, ahead of the
	// trailing Details / Remove line.
	wantMenu := []string{byName["edit-grocery"].ID.String(), byName["edit-item"].ID.String()}
	if !slices.Equal(list.Display.Slots.RowActions, wantMenu) {
		t.Errorf("pending-groceries row actions = %v, want edit-grocery placed ahead of edit-item",
			list.Display.Slots.RowActions)
	}
	pantry := byName["pantry"]
	if pantry.Display == nil || pantry.Display.Slots.Add != byName["stock-item"].ID.String() {
		t.Fatalf("pantry add slot = %+v, want stock-item", pantry.Display)
	}
	wantActions := []string{byName["add-to-list"].ID.String(), byName["merge-item"].ID.String()}
	if !slices.Equal(pantry.Display.Slots.RowActions, wantActions) {
		t.Errorf("pantry row actions = %v, want add-to-list leading merge-item", pantry.Display.Slots.RowActions)
	}
	for _, p := range byName["stock-item"].Params {
		if p.Inline {
			t.Errorf("stock-item param %s is inline, want the dialog-only add", p.Name)
		}
	}

	// The item fields are creatable pickers: catalog suggestions, with typing past them accepted —
	// both submit the name, which the resolve-or-create SQL serves. The quick add's store gets the
	// same treatment over the shops already on file.
	creatable := []struct{ formula, param, table string }{
		{"stock-item", "item", "item_options"},
		{"add-grocery", "item", "item_options"},
		{"add-grocery", "category", "categories"},
		{"add-grocery", "store", "store_options"},
		{"edit-grocery", "store", "store_options"},
		{"edit-item", "category", "categories"},
		{"edit-catalog-item", "category", "categories"},
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

	// Picking an existing item fills the dialog's category and habit from the options view, so the
	// dialog shows the catalog's filing and a change to either re-files deliberately.
	for _, p := range byName["add-grocery"].Params {
		if p.Name != "item" {
			continue
		}
		want := map[string]string{"category": "category", "habit": "habit"}
		if p.Lookup == nil || !maps.Equal(p.Lookup.Fill, want) {
			t.Errorf("add-grocery item fill = %v, want %v", p.Lookup, want)
		}
	}
}

// TestShoppingListOpensOnStaples pins the habit filter both list views carry: an omitted habit binds
// the staple default and hides a one-off, an empty pick widens to everything, and a picked habit narrows
// to it.
func TestShoppingListOpensOnStaples(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk"})
	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "vacuum cleaner", "habit": "one-off"})

	staples := runFormula(t, deps, userID, bdbID, "pending-groceries", nil)
	if len(staples.Result.Rows) != 1 || cell(t, staples.Result, 0, "name") != "milk" {
		t.Fatalf("default rows = %v, want the staple alone — the list opens on the everyday run", staples.Result.Rows)
	}
	if all := runFormula(t, deps, userID, bdbID, "pending-groceries",
		map[string]any{"habit": []any{}}); len(all.Result.Rows) != 2 {
		t.Errorf("habit=[] rows = %d, want both lines — an empty pick is no constraint", len(all.Result.Rows))
	}
	oneOffs := runFormula(t, deps, userID, bdbID, "pending-groceries", map[string]any{"habit": []any{"one-off"}})
	if len(oneOffs.Result.Rows) != 1 || cell(t, oneOffs.Result, 0, "name") != "vacuum cleaner" {
		t.Errorf("habit=[one-off] rows = %v, want the vacuum cleaner alone", oneOffs.Result.Rows)
	}

	// The pantry carries the same default: the one-off is out of its default view even with show_all.
	pantry := runFormula(t, deps, userID, bdbID, "pantry", map[string]any{"show_all": true})
	if len(pantry.Result.Rows) != 1 || cell(t, pantry.Result, 0, "name") != "milk" {
		t.Errorf("pantry show_all rows = %v, want the staple alone until habit widens", pantry.Result.Rows)
	}
	widened := runFormula(t, deps, userID, bdbID, "pantry",
		map[string]any{"show_all": true, "habit": []any{"staple", "one-off"}})
	if len(widened.Result.Rows) != 2 {
		t.Errorf("pantry habit=[staple, one-off] rows = %d, want both items", len(widened.Result.Rows))
	}
}

// TestCategoryFilterNarrowsToAnyPick pins the category multi-select on both views: an omitted pick binds
// an empty array and narrows nothing, and a set of ids keeps a row matching any of them.
func TestCategoryFilterNarrowsToAnyPick(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk", "category": "dairy"})
	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "sponge", "category": "cleaning"})
	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "baguette", "category": "bakery"})
	ids := execSQL(t, deps, userID, bdbID, "SELECT id, name FROM categories ORDER BY id", dbdomain.IntentQuery)
	dairy := float64(cell(t, ids, 0, "id").(int64))
	bakery := float64(cell(t, ids, 2, "id").(int64))

	picked := runFormula(t, deps, userID, bdbID, "pending-groceries",
		map[string]any{"categories": []any{dairy, bakery}})
	names := make([]string, 0, len(picked.Result.Rows))
	for i := range picked.Result.Rows {
		name, _ := cell(t, picked.Result, i, "name").(string)
		names = append(names, name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"baguette", "milk"}) {
		t.Errorf("list rows for two categories = %v, want the dairy and bakery lines", names)
	}
	if all := runFormula(t, deps, userID, bdbID, "pending-groceries", nil); len(all.Result.Rows) != 3 {
		t.Errorf("unfiltered rows = %d, want 3 — an omitted pick must not narrow", len(all.Result.Rows))
	}

	pantry := runFormula(t, deps, userID, bdbID, "pantry",
		map[string]any{"show_all": true, "categories": []any{dairy}})
	if len(pantry.Result.Rows) != 1 || cell(t, pantry.Result, 0, "name") != "milk" {
		t.Errorf("pantry rows for dairy = %v, want milk alone", pantry.Result.Rows)
	}
}

// TestAddGroceryRefilesOnlyOnDeliberateValues pins the add dialog's catalog write: a new item takes the
// category and habit given (staple when none), an existing one keeps both when the fields are empty, and
// a filled field re-files it. The habit vocabulary is closed twice over — the param's options reject a
// stray value before the SQL, and the column's CHECK refuses one written past the formulas.
func TestAddGroceryRefilesOnlyOnDeliberateValues(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	itemOf := func(name string) (category, habit any) {
		t.Helper()
		row := execSQL(t, deps, userID, bdbID,
			"SELECT c.name AS category, i.habit FROM items i LEFT JOIN categories c ON c.id = i.category_id"+
				" WHERE i.name = '"+name+"'", dbdomain.IntentQuery)
		return cell(t, row, 0, "category"), cell(t, row, 0, "habit")
	}

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk", "category": "dairy"})
	if category, habit := itemOf("milk"); category != "dairy" || habit != "staple" {
		t.Fatalf("new item = %v / %v, want the category given and the staple default", category, habit)
	}

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "milk"})
	if category, habit := itemOf("milk"); category != "dairy" || habit != "staple" {
		t.Errorf("after an empty re-add = %v / %v, want the item untouched", category, habit)
	}

	runFormula(t, deps, userID, bdbID, "add-grocery",
		map[string]any{"item": "milk", "category": "bakery", "habit": "occasional"})
	if category, habit := itemOf("milk"); category != "bakery" || habit != "occasional" {
		t.Errorf("after a filled re-add = %v / %v, want both re-filed", category, habit)
	}

	_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "add-grocery",
		dbservice.FormulaRunInput{Params: map[string]any{"item": "bogus", "habit": "weekly"}})
	if err == nil {
		t.Error("add-grocery with an off-vocabulary habit succeeded, want the options to reject it")
	}
	_, err = deps.querySvc.Execute(t.Context(), userID, bdbID,
		"INSERT INTO items (name, habit) VALUES ('bogus', 'weekly')", nil, dbdomain.IntentExecute)
	if err == nil {
		t.Error("raw insert with an off-vocabulary habit succeeded, want the CHECK to refuse it")
	}
}

// TestInboxRoundTrip drives the inbox tab end to end: a dropped list shows as a dated, line-counted card
// with the text as its body, a blank drop inserts nothing, marking it sorted keeps it visible for the
// undo window, clearing removes the sorted entry and leaves the waiting one, and the detail opens the
// full text.
func TestInboxRoundTrip(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)
	today := time.Now().UTC().Format(time.DateOnly)

	text := "Pour ce week-end:\n- 500g de farine\n- 3 oeufs\n- beurre\n- papier toilette (Monoprix)\n"
	runFormula(t, deps, userID, bdbID, "drop-list", map[string]any{"text": text})
	runFormula(t, deps, userID, bdbID, "drop-list", map[string]any{"text": "  \n "})
	runFormula(t, deps, userID, bdbID, "drop-list", map[string]any{"text": "still waiting"})

	cards := runFormula(t, deps, userID, bdbID, "inbox-cards", nil)
	if len(cards.Result.Rows) != 2 {
		t.Fatalf("inbox rows = %d, want the two real drops and nothing for the blank one", len(cards.Result.Rows))
	}
	// Newest first, so the "still waiting" drop leads and the five-line list follows.
	if title := cell(t, cards.Result, 1, "title"); title != "Dropped "+today+" · 5 lines" {
		t.Errorf("title = %q, want the date and the line count", title)
	}
	if body := cell(t, cards.Result, 1, "body"); body != text {
		t.Errorf("body = %q, want the text as written", body)
	}
	listID := float64(cell(t, cards.Result, 1, "rowid").(int64))

	detail := runFormula(t, deps, userID, bdbID, "dropped-list", map[string]any{"rowid": listID})
	if got := cell(t, detail.Result, 0, "text"); got != text {
		t.Errorf("detail text = %q, want the full text", got)
	}

	runFormula(t, deps, userID, bdbID, "mark-sorted", map[string]any{"rowid": listID})
	lingering := runFormula(t, deps, userID, bdbID, "inbox-cards", nil)
	if len(lingering.Result.Rows) != 2 {
		t.Fatalf("rows right after sorting = %d, want the sorted entry lingering for the undo window",
			len(lingering.Result.Rows))
	}
	if sortedAt := cell(t, lingering.Result, 1, "sorted_at"); sortedAt == nil {
		t.Error("sorted_at after marking = nil, want it stamped")
	}

	runFormula(t, deps, userID, bdbID, "clear-sorted", nil)
	left := runFormula(t, deps, userID, bdbID, "inbox-cards", map[string]any{"show_sorted": true})
	if len(left.Result.Rows) != 1 || cell(t, left.Result, 0, "body") != "still waiting" {
		t.Errorf("rows after clearing = %v, want only the waiting entry", left.Result.Rows)
	}
}

// TestClearSortedRespectsTheCutoff pins the clear action's day: an entry sorted after the given day
// stays, one sorted on or before it goes, and a waiting entry is never touched whatever the day.
func TestClearSortedRespectsTheCutoff(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	for _, text := range []string{"old", "recent", "waiting"} {
		runFormula(t, deps, userID, bdbID, "drop-list", map[string]any{"text": text})
	}
	execSQL(t, deps, userID, bdbID,
		"UPDATE inbox SET sorted_at = '2020-01-01 10:00:00' WHERE text = 'old'", dbdomain.IntentExecute)
	execSQL(t, deps, userID, bdbID,
		"UPDATE inbox SET sorted_at = CURRENT_TIMESTAMP WHERE text = 'recent'", dbdomain.IntentExecute)

	runFormula(t, deps, userID, bdbID, "clear-sorted", map[string]any{"before": "2020-01-01"})
	left := execSQL(t, deps, userID, bdbID, "SELECT text FROM inbox ORDER BY text", dbdomain.IntentQuery)
	if len(left.Rows) != 2 || cell(t, left, 0, "text") != "recent" || cell(t, left, 1, "text") != "waiting" {
		t.Errorf("after clearing up to 2020-01-01 = %v, want the old entry gone and the other two kept", left.Rows)
	}

	runFormula(t, deps, userID, bdbID, "clear-sorted", nil)
	left = execSQL(t, deps, userID, bdbID, "SELECT text FROM inbox", dbdomain.IntentQuery)
	if len(left.Rows) != 1 || cell(t, left, 0, "text") != "waiting" {
		t.Errorf("after clearing everything sorted = %v, want the waiting entry alone", left.Rows)
	}
}

// TestRestockSuggestionsListStaplesThatRanOut pins the assistant's restock view: a staple shows once it
// has no batch and no pending line, bought ones lead and never-bought ones trail, a line back on the
// list drops it, and a one-off never shows however it was bought.
func TestRestockSuggestionsListStaplesThatRanOut(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	tossLastBatch := func() {
		t.Helper()
		batch := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM inventory", dbdomain.IntentQuery)
		runFormula(t, deps, userID, bdbID, "delete-batch",
			map[string]any{"rowid": float64(cell(t, batch, 0, "id").(int64))})
	}
	names := func() []string {
		t.Helper()
		rows := runFormula(t, deps, userID, bdbID, "restock-suggestions", nil)
		out := make([]string, 0, len(rows.Result.Rows))
		for i := range rows.Result.Rows {
			name, _ := cell(t, rows.Result, i, "name").(string)
			out = append(out, name)
		}
		return out
	}

	milk := buyItem(t, deps, userID, bdbID, "milk", "")
	if got := names(); len(got) != 0 {
		t.Fatalf("suggestions with milk in stock = %v, want none", got)
	}
	tossLastBatch()
	if got := names(); !slices.Equal(got, []string{"milk"}) {
		t.Errorf("suggestions once milk ran out = %v, want milk", got)
	}

	// Stocked off the list and used up: a staple that was never bought through the list still counts,
	// after the ones with a purchase date.
	runFormula(t, deps, userID, bdbID, "stock-item", map[string]any{"item": "salt"})
	tossLastBatch()
	if got := names(); !slices.Equal(got, []string{"milk", "salt"}) {
		t.Errorf("suggestions = %v, want the bought staple leading the never-bought one", got)
	}

	runFormula(t, deps, userID, bdbID, "add-to-list", map[string]any{"rowid": milk})
	if got := names(); !slices.Equal(got, []string{"salt"}) {
		t.Errorf("suggestions with milk back on the list = %v, want salt alone", got)
	}

	runFormula(t, deps, userID, bdbID, "add-grocery", map[string]any{"item": "vacuum cleaner", "habit": "one-off"})
	added := execSQL(t, deps, userID, bdbID, "SELECT MAX(id) AS id FROM groceries", dbdomain.IntentQuery)
	runFormula(t, deps, userID, bdbID, "mark-bought",
		map[string]any{"rowid": float64(cell(t, added, 0, "id").(int64))})
	tossLastBatch()
	if got := names(); !slices.Equal(got, []string{"salt"}) {
		t.Errorf("suggestions after a one-off ran out = %v, want it left out", got)
	}
}

// TestInboxDisplayWiring pins the inbox tab's slots and the two standalone formulas around it: the cards
// add through the drop dialog, open on the full-text detail, and carry the mark-sorted menu entry; the
// clear action is a visible top-level formula, and the restock view is assistant-only.
func TestInboxDisplayWiring(t *testing.T) {
	deps, userID, bdbID := applyShoppingPantry(t)

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	byName := make(map[string]*dbdomain.Formula, len(formulas))
	for _, f := range formulas {
		byName[f.Name] = f
	}

	inbox := byName["inbox-cards"]
	if inbox.Display == nil || inbox.Display.Mode != dbdomain.DisplayModeCards {
		t.Fatalf("inbox-cards display = %+v, want cards", inbox.Display)
	}
	if inbox.Display.Slots.Add != byName["drop-list"].ID.String() {
		t.Errorf("inbox add slot = %v, want drop-list", inbox.Display.Slots.Add)
	}
	if inbox.Display.Slots.Detail != byName["dropped-list"].ID.String() {
		t.Errorf("inbox detail slot = %v, want dropped-list", inbox.Display.Slots.Detail)
	}
	if want := []string{byName["mark-sorted"].ID.String()}; !slices.Equal(inbox.Display.Slots.RowActions, want) {
		t.Errorf("inbox row actions = %v, want mark-sorted alone", inbox.Display.Slots.RowActions)
	}
	for _, p := range byName["drop-list"].Params {
		if p.Inline {
			t.Errorf("drop-list param %s is inline, want the dialog-only textarea", p.Name)
		}
	}

	if clearSorted := byName["clear-sorted"]; clearSorted.Hidden || clearSorted.AttachedToFormulaID != nil {
		t.Errorf("clear-sorted hidden=%v attached=%v, want a visible top-level action", clearSorted.Hidden,
			clearSorted.AttachedToFormulaID)
	}
	if restock := byName["restock-suggestions"]; !restock.Hidden || restock.AttachedToFormulaID != nil {
		t.Errorf("restock-suggestions hidden=%v attached=%v, want an assistant-only top-level view",
			restock.Hidden, restock.AttachedToFormulaID)
	}
}
