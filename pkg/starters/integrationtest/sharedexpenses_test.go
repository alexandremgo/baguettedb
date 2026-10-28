//go:build integration

package integrationtest

import (
	"encoding/json"
	"maps"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	dbdomain "github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/embedded"
)

// applySharedExpenses applies the embedded shared-expenses starter for a fresh user and returns the wired
// services with the new bdb's coordinates, ready for formula runs.
func applySharedExpenses(t *testing.T) (deps engineDeps, userID, bdbID uuid.UUID) {
	t.Helper()
	source, err := embedded.NewSource()
	if err != nil {
		t.Fatalf("NewSource() error = %v", err)
	}
	svc, deps := setupEngine(t, source)
	userID = uuid.New()
	bdbID, err = svc.CreateFromStarter(t.Context(), userID, testRoles, "shared-expenses", "", "")
	if err != nil {
		t.Fatalf("CreateFromStarter(shared-expenses) error = %v", err)
	}
	return deps, userID, bdbID
}

// TestSharedExpensesRecurringRoundTrip drives a recurring bill through its whole life: created with the
// omitted params falling back (next_due = start date, interval_n = its declared default), applied once
// with the payer/share templates copied onto the materialized expense, and the due date advanced one
// month.
func TestSharedExpensesRecurringRoundTrip(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Alice"})
	runFormula(t, deps, userID, bdbID, "add-recurring", map[string]any{
		"description":  "Rent",
		"total_amount": 900.0,
		"frequency":    "monthly",
		"start_date":   "2026-01-15",
		"category":     "Housing",
		"payers":       []any{map[string]any{"member": 1.0, "amount": 900.0}},
		"shares":       []any{map[string]any{"member": 1.0, "amount": 900.0}},
	})

	stored := execSQL(t, deps, userID, bdbID, `
		SELECT r.interval_n, r.next_due,
		       (SELECT c.name FROM categories c WHERE c.id = r.category_id) AS category
		FROM recurring_expenses r`, dbdomain.IntentQuery)
	if got := len(stored.Rows); got != 1 {
		t.Fatalf("recurring rows = %d, want 1", got)
	}
	if v := cell(t, stored, 0, "interval_n"); v != int64(1) {
		t.Errorf("interval_n = %v, want the declared default 1", v)
	}
	if v := cell(t, stored, 0, "next_due"); v != "2026-01-15" {
		t.Errorf("next_due = %v, want the start date fallback", v)
	}
	if v := cell(t, stored, 0, "category"); v != "Housing" {
		t.Errorf("category = %v, want the typed name created and resolved", v)
	}

	runFormula(t, deps, userID, bdbID, "apply-recurring",
		map[string]any{"rowid": float64(1), "date": "2026-01-15"})

	applied := execSQL(t, deps, userID, bdbID, `
		SELECT e.date, e.total_amount,
		       (SELECT SUM(ep.amount_paid) FROM expense_payers ep WHERE ep.expense_id = e.id) AS paid,
		       (SELECT SUM(es.share_amount) FROM expense_shares es WHERE es.expense_id = e.id) AS owed,
		       (SELECT r.next_due FROM recurring_expenses r WHERE r.id = e.recurring_id) AS next_due
		FROM expenses e`, dbdomain.IntentQuery)
	if got := len(applied.Rows); got != 1 {
		t.Fatalf("expenses after apply = %d, want 1", got)
	}
	if v := cell(t, applied, 0, "date"); v != "2026-01-15" {
		t.Errorf("expense date = %v, want the apply date", v)
	}
	if paid, owed := cell(t, applied, 0, "paid"), cell(t, applied, 0, "owed"); paid != 900.0 || owed != 900.0 {
		t.Errorf("paid/owed sums = %v/%v, want the templates copied (900/900)", paid, owed)
	}
	if v := cell(t, applied, 0, "next_due"); v != "2026-02-15" {
		t.Errorf("next_due = %v, want one month advanced", v)
	}
}

// TestSharedExpensesApplyForeignFrequencyFreezes pins the advance CASE's ELSE guard: the action's options
// pin frequency, but a row written directly can carry anything — applying it must still materialize the
// expense and leave next_due frozen rather than NULLing a NOT NULL column and aborting the run.
func TestSharedExpensesApplyForeignFrequencyFreezes(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	execSQL(t, deps, userID, bdbID, `
		INSERT INTO recurring_expenses (description, total_amount, frequency, start_date, next_due)
		VALUES ('Gym', 30, 'fortnightly', '2026-01-01', '2026-01-01')`, dbdomain.IntentExecute)

	runFormula(t, deps, userID, bdbID, "apply-recurring",
		map[string]any{"rowid": float64(1), "date": "2026-01-01"})

	res := execSQL(t, deps, userID, bdbID,
		"SELECT (SELECT COUNT(*) FROM expenses) AS expenses, r.next_due FROM recurring_expenses r",
		dbdomain.IntentQuery)
	if v := cell(t, res, 0, "expenses"); v != int64(1) {
		t.Errorf("expenses = %v, want the occurrence materialized despite the foreign frequency", v)
	}
	if v := cell(t, res, 0, "next_due"); v != "2026-01-01" {
		t.Errorf("next_due = %v, want it frozen (still visibly due) for a foreign frequency", v)
	}
}

// TestSharedExpensesCreatableCategory drives the creatable category through its whole contract: a typed
// name creates the row, a case-and-whitespace twin resolves to it instead of forking the list, an omitted
// category stays NULL, the browse view reads the name back, and the edit clears or creates likewise.
func TestSharedExpensesCreatableCategory(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Alice"})
	runFormula(t, deps, userID, bdbID, "add-expense",
		map[string]any{"description": "Fruit", "amount": 12.5, "paid_by": 1.0, "category": "groceries"})
	runFormula(t, deps, userID, bdbID, "add-expense",
		map[string]any{"description": "Bread", "amount": 3.0, "paid_by": 1.0, "category": " Groceries "})
	runFormula(t, deps, userID, bdbID, "add-expense",
		map[string]any{"description": "Taxi", "amount": 20.0, "paid_by": 1.0})

	cats := execSQL(t, deps, userID, bdbID, "SELECT name FROM categories ORDER BY id", dbdomain.IntentQuery)
	if len(cats.Rows) != 1 || cell(t, cats, 0, "name") != "groceries" {
		t.Fatalf("categories = %v rows, want only the first spelling — a case twin must not fork the list",
			len(cats.Rows))
	}

	list := runFormula(t, deps, userID, bdbID, "expenses-list", nil)
	if got := len(list.Result.Rows); got != 3 {
		t.Fatalf("expenses-list rows = %d, want 3", got)
	}
	// Same date, so newest-first falls back to id: Taxi, Bread, Fruit.
	if v := cell(t, list.Result, 0, "category"); v != nil {
		t.Errorf("Taxi category = %v, want NULL for an omitted category", v)
	}
	if v := cell(t, list.Result, 1, "category"); v != "groceries" {
		t.Errorf("Bread category = %v, want the resolved existing spelling", v)
	}

	runFormula(t, deps, userID, bdbID, "edit-expense",
		map[string]any{"rowid": float64(1), "description": "Fruit"})
	runFormula(t, deps, userID, bdbID, "edit-expense",
		map[string]any{"rowid": float64(2), "description": "Bread", "category": "Bakery"})

	edited := execSQL(t, deps, userID, bdbID, `
		SELECT e.id, (SELECT c.name FROM categories c WHERE c.id = e.category_id) AS category
		FROM expenses e ORDER BY e.id`, dbdomain.IntentQuery)
	if v := cell(t, edited, 0, "category"); v != nil {
		t.Errorf("edited Fruit category = %v, want cleared by the omitted field", v)
	}
	if v := cell(t, edited, 1, "category"); v != "Bakery" {
		t.Errorf("edited Bread category = %v, want the newly created name", v)
	}
}

// TestSharedExpensesCustomSplit pins add-expense's dual path: filled payers AND shares replace the equal
// split with the explicit rows (the worked-example basket) — a supplied paid_by binding harmlessly on the
// custom face — while a one-sided fill is rejected up front, since the tick makes both groups required.
func TestSharedExpensesCustomSplit(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Alice"})
	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Bob"})
	runFormula(t, deps, userID, bdbID, "add-expense", map[string]any{
		"description": "Basket", "amount": 32.18, "paid_by": 1.0, "custom_split": true,
		"payers": []any{
			map[string]any{"member": 1.0, "amount": 25.0},
			map[string]any{"member": 2.0, "amount": 7.18},
		},
		"shares": []any{
			map[string]any{"member": 1.0, "amount": 11.83},
			map[string]any{"member": 2.0, "amount": 20.35},
		},
	})

	basket := execSQL(t, deps, userID, bdbID, `
		SELECT (SELECT COUNT(*) FROM expense_payers) AS payer_rows,
		       (SELECT amount_paid FROM expense_payers WHERE member_id = 2) AS bob_paid,
		       (SELECT share_amount FROM expense_shares WHERE member_id = 2) AS bob_share,
		       (SELECT SUM(share_amount) FROM expense_shares) AS share_sum,
		       e.total_amount
		FROM expenses e`, dbdomain.IntentQuery)
	if v := cell(t, basket, 0, "payer_rows"); v != int64(2) {
		t.Errorf("payer rows = %v, want the two explicit payers, not the equal-split payer", v)
	}
	if paid, share := cell(t, basket, 0, "bob_paid"), cell(t, basket, 0, "bob_share"); paid != 7.18 || share != 20.35 {
		t.Errorf("Bob paid/share = %v/%v, want the explicit 7.18/20.35 rows", paid, share)
	}
	total, sum := cell(t, basket, 0, "total_amount"), cell(t, basket, 0, "share_sum")
	if total != 32.18 || sum != 32.18 {
		t.Errorf("total/share-sum = %v/%v, want the canonical amount both places", total, sum)
	}

	// One-sided custom fill: the tick makes both groups required, so the missing side rejects the run
	// before any SQL — no silent fallback to the equal split.
	_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "add-expense",
		dbservice.FormulaRunInput{Params: map[string]any{
			"description": "Half filled", "amount": 10.0, "custom_split": true,
			"payers": []any{map[string]any{"member": 2.0, "amount": 10.0}},
		}})
	if err == nil || !strings.Contains(err.Error(), "shares") {
		t.Fatalf("half-filled custom split error = %v, want the missing shares group named", err)
	}
	leftover := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM expenses WHERE description = 'Half filled'", dbdomain.IntentQuery)
	if v := cell(t, leftover, 0, "n"); v != int64(0) {
		t.Errorf("half-filled expenses = %v, want the rejected run to have written nothing", v)
	}
}

// TestSharedExpensesAddExpenseTwoFaces pins the conditional requiredness both ways: with custom split on,
// paid_by is not required — both groups alone add the expense (the app hides the field, so it can never
// send it) — and with it off, an omitted paid_by is rejected as missing.
func TestSharedExpensesAddExpenseTwoFaces(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Alice"})
	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Bob"})

	runFormula(t, deps, userID, bdbID, "add-expense", map[string]any{
		"description": "Custom without payer", "amount": 30.0, "custom_split": true,
		"payers": []any{map[string]any{"member": 1.0, "amount": 30.0}},
		"shares": []any{map[string]any{"member": 2.0, "amount": 30.0}},
	})
	stored := execSQL(t, deps, userID, bdbID, `
		SELECT (SELECT member_id FROM expense_payers) AS payer,
		       (SELECT member_id FROM expense_shares) AS ower
		FROM expenses`, dbdomain.IntentQuery)
	if payer, ower := cell(t, stored, 0, "payer"), cell(t, stored, 0, "ower"); payer != int64(1) || ower != int64(2) {
		t.Errorf("payer/ower = %v/%v, want the explicit rows with no paid_by supplied", payer, ower)
	}

	_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "add-expense",
		dbservice.FormulaRunInput{Params: map[string]any{"description": "Equal without payer", "amount": 10.0}})
	if err == nil || !strings.Contains(err.Error(), "paid_by") {
		t.Fatalf("equal split without paid_by error = %v, want the missing payer named", err)
	}
}

// TestSharedExpensesSettleUpFromBalanceRow drives the rebalanced settle-up: the action runs from the
// paying member's Balances row (the reserved rowid binds from_member), the receiver is picked, and the
// recorded settlement moves both balances to zero — never further apart.
func TestSharedExpensesSettleUpFromBalanceRow(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Alice"})
	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Bob"})
	runFormula(t, deps, userID, bdbID, "add-expense",
		map[string]any{"description": "Dinner", "amount": 30.0, "paid_by": 1.0})

	before := runFormula(t, deps, userID, bdbID, "members", nil)
	if a, b := cell(t, before.Result, 0, "balance"), cell(t, before.Result, 1, "balance"); a != 15.0 || b != -15.0 {
		t.Fatalf("balances before settling = %v/%v, want +15 (Alice is owed) and -15 (Bob owes)", a, b)
	}

	runFormula(t, deps, userID, bdbID, "record-settlement",
		map[string]any{"rowid": float64(2), "to_member": 1.0, "amount": 15.0})

	stored := execSQL(t, deps, userID, bdbID,
		"SELECT from_member, to_member, amount, date FROM settlements", dbdomain.IntentQuery)
	if got := len(stored.Rows); got != 1 {
		t.Fatalf("settlements = %d, want 1", got)
	}
	from, to := cell(t, stored, 0, "from_member"), cell(t, stored, 0, "to_member")
	if from != int64(2) || to != int64(1) {
		t.Errorf("settlement from/to = %v/%v, want the tapped row (2) paying the picked member (1)", from, to)
	}
	if v := cell(t, stored, 0, "date"); v == nil || v == "" {
		t.Errorf("settlement date = %v, want today filled in by the omitted-date default", v)
	}

	after := runFormula(t, deps, userID, bdbID, "members", nil)
	for i := range after.Result.Rows {
		if v := cell(t, after.Result, i, "balance"); v != 0.0 {
			t.Errorf("balance row %d = %v, want 0 — the settlement must square both sides", i, v)
		}
	}
}

// TestSharedExpensesListSearch pins the expenses-list query filter: it matches on description and on
// category name, and an omitted query keeps the whole list.
func TestSharedExpensesListSearch(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Alice"})
	runFormula(t, deps, userID, bdbID, "add-expense",
		map[string]any{"description": "Fruit", "amount": 12.5, "paid_by": 1.0, "category": "groceries"})
	runFormula(t, deps, userID, bdbID, "add-expense",
		map[string]any{"description": "Taxi", "amount": 20.0, "paid_by": 1.0})

	all := runFormula(t, deps, userID, bdbID, "expenses-list", nil)
	if got := len(all.Result.Rows); got != 2 {
		t.Fatalf("unfiltered rows = %d, want 2", got)
	}
	byCategory := runFormula(t, deps, userID, bdbID, "expenses-list", map[string]any{"query": "grocer"})
	if got := len(byCategory.Result.Rows); got != 1 || cell(t, byCategory.Result, 0, "description") != "Fruit" {
		t.Fatalf("query=grocer rows = %d, want only Fruit via its category name", got)
	}
	byDescription := runFormula(t, deps, userID, bdbID, "expenses-list", map[string]any{"query": "tax"})
	if got := len(byDescription.Result.Rows); got != 1 || cell(t, byDescription.Result, 0, "description") != "Taxi" {
		t.Fatalf("query=tax rows = %d, want only Taxi via its description", got)
	}
}

// TestSharedExpensesListPeriodFilter pins the expenses-list month/year scoping, mirroring the members
// view's semantics: empty means everything, a year alone scopes that year, and a month alone means that
// month of the current year — so a month from a past year needs both.
func TestSharedExpensesListPeriodFilter(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Alice"})
	for _, e := range []struct {
		description, date string
	}{
		{"August groceries", "2026-08-05"},
		{"September groceries", "2026-09-05"},
		{"Old September", "2025-09-05"},
	} {
		runFormula(t, deps, userID, bdbID, "add-expense",
			map[string]any{"description": e.description, "amount": 10.0, "paid_by": 1.0, "date": e.date})
	}

	if got := len(runFormula(t, deps, userID, bdbID, "expenses-list", nil).Result.Rows); got != 3 {
		t.Fatalf("unfiltered rows = %d, want all 3", got)
	}
	byMonth := runFormula(t, deps, userID, bdbID, "expenses-list", map[string]any{"month": 9.0})
	if got := len(byMonth.Result.Rows); got != 1 || cell(t, byMonth.Result, 0, "description") != "September groceries" {
		t.Fatalf("month=9 rows = %d, want only the current year's September", got)
	}
	byYear := runFormula(t, deps, userID, bdbID, "expenses-list", map[string]any{"year": 2025.0})
	if got := len(byYear.Result.Rows); got != 1 || cell(t, byYear.Result, 0, "description") != "Old September" {
		t.Fatalf("year=2025 rows = %d, want only the old expense", got)
	}
	past := runFormula(t, deps, userID, bdbID, "expenses-list", map[string]any{"month": 9.0, "year": 2025.0})
	if got := len(past.Result.Rows); got != 1 || cell(t, past.Result, 0, "description") != "Old September" {
		t.Fatalf("month=9 year=2025 rows = %d, want only the old September", got)
	}
}

// TestSharedExpensesMonthFilterLabeledOptions pins the month filters' labeled sets: the Expenses and
// Members views both declare all twelve months as {value, label} options — value the integer the SQL
// compares, label the month name the select shows — and the set still enforces: a month outside 1-12 is
// rejected before the SQL runs. The hidden expenses-for-period keeps a bare typed param.
func TestSharedExpensesMonthFilterLabeledOptions(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	formulas, err := deps.formulaSvc.ListFormulas(t.Context(), userID, bdbID)
	if err != nil {
		t.Fatalf("ListFormulas() error = %v", err)
	}
	monthParam := func(formula string) *dbdomain.FormulaParam {
		for _, f := range formulas {
			if f.Name != formula {
				continue
			}
			for i := range f.Params {
				if f.Params[i].Name == "month" {
					return &f.Params[i]
				}
			}
		}
		t.Fatalf("formula %s declares no month param", formula)
		return nil
	}

	for _, view := range []string{"expenses-list", "members"} {
		p := monthParam(view)
		if len(p.Options) != 12 {
			t.Fatalf("%s month options = %d entries, want all 12 months", view, len(p.Options))
		}
		for i, opt := range p.Options {
			want := dbdomain.FormulaOption{Value: strconv.Itoa(i + 1), Label: time.Month(i + 1).String()}
			if opt != want {
				t.Errorf("%s month options[%d] = %+v, want %+v", view, i, opt, want)
			}
		}
	}
	if p := monthParam("expenses-for-period"); len(p.Options) != 0 {
		t.Errorf("expenses-for-period month options = %+v, want the bare typed param kept", p.Options)
	}

	if _, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "expenses-list",
		dbservice.FormulaRunInput{Params: map[string]any{"month": 13.0}}); err == nil {
		t.Error("month=13 ran, want the closed set to reject it")
	}
}

// TestSharedExpensesMemberSettlementsDetail pins the Balances row detail: each side sees the settlement
// phrased from their perspective with the direction and counterparty carried raw, every row is
// deletable (rowid = the settlement id), and deleting one moves the balances back apart.
func TestSharedExpensesMemberSettlementsDetail(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Alice"})
	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Bob"})
	runFormula(t, deps, userID, bdbID, "add-expense",
		map[string]any{"description": "Dinner", "amount": 30.0, "paid_by": 1.0, "date": "2026-08-10"})
	runFormula(t, deps, userID, bdbID, "record-settlement",
		map[string]any{"rowid": float64(2), "to_member": 1.0, "amount": 15.0, "note": "cash", "date": "2026-08-12"})

	alice := runFormula(t, deps, userID, bdbID, "member-settlements", map[string]any{"rowid": float64(1)})
	if got := len(alice.Result.Rows); got != 1 {
		t.Fatalf("Alice settlement rows = %d, want just the payback — expenses stay on their own tab", got)
	}
	if v := cell(t, alice.Result, 0, "line"); v != "2026-08-12 · Paid back by Bob · 15.00 — cash" {
		t.Errorf("line = %v, want the settlement phrased from the receiver's side, note included", v)
	}
	id, dir := cell(t, alice.Result, 0, "rowid"), cell(t, alice.Result, 0, "direction")
	if id != int64(1) || dir != "received" {
		t.Errorf("rowid/direction = %v/%v, want the settlement id (deletable row) marked received", id, dir)
	}

	bob := runFormula(t, deps, userID, bdbID, "member-settlements", map[string]any{"rowid": float64(2)})
	if v := cell(t, bob.Result, 0, "line"); v != "2026-08-12 · Paid back Alice · 15.00 — cash" {
		t.Errorf("line = %v, want the settlement phrased from the payer's side", v)
	}
	bobDir, cp := cell(t, bob.Result, 0, "direction"), cell(t, bob.Result, 0, "counterparty")
	if bobDir != "sent" || cp != "Alice" {
		t.Errorf("direction/counterparty = %v/%v, want sent to Alice", bobDir, cp)
	}

	runFormula(t, deps, userID, bdbID, "delete-settlement", map[string]any{"rowid": float64(1)})
	after := runFormula(t, deps, userID, bdbID, "members", nil)
	if a, b := cell(t, after.Result, 0, "balance"), cell(t, after.Result, 1, "balance"); a != 15.0 || b != -15.0 {
		t.Errorf("balances after deleting the settlement = %v/%v, want the debt back (+15/-15)", a, b)
	}
	if got := len(runFormula(t, deps, userID, bdbID, "member-settlements",
		map[string]any{"rowid": float64(1)}).Result.Rows); got != 0 {
		t.Errorf("Alice settlement rows after delete = %d, want none left", got)
	}
}

// moneyMap decodes a json_group_object cell — e.g. `{"Alice":600,"Bob":300}` — into member-name →
// amount pairs, failing the test when the cell is not a string or does not parse.
func moneyMap(t *testing.T, v any) map[string]float64 {
	t.Helper()

	strV, ok := v.(string)
	if !ok {
		t.Fatalf("cell = %T, want the JSON text json_group_object emits", v)
	}

	var m map[string]float64
	if err := json.Unmarshal([]byte(strV), &m); err != nil {
		t.Fatalf("cell %q does not parse as name -> amount pairs: %v", strV, err)
	}
	return m
}

// TestSharedExpensesEditAndStopRecurring drives the new bill lifecycle: an edit with both templates
// filled replaces them and re-derives the total, a one-sided fill changes no amounts (the invariant
// can't be half-broken), and stopping the bill removes it from the active list without touching its
// already materialized expenses.
func TestSharedExpensesEditAndStopRecurring(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Alice"})
	runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": "Bob"})
	runFormula(t, deps, userID, bdbID, "add-recurring", map[string]any{
		"description":  "Internet",
		"total_amount": 60.0,
		"frequency":    "monthly",
		"start_date":   "2026-03-01",
		"payers":       []any{map[string]any{"member": 1.0, "amount": 60.0}},
		"shares": []any{
			map[string]any{"member": 1.0, "amount": 30.0},
			map[string]any{"member": 2.0, "amount": 30.0},
		},
	})

	runFormula(t, deps, userID, bdbID, "edit-recurring", map[string]any{
		"rowid":       float64(1),
		"description": "Fiber internet",
		"interval_n":  float64(2),
		"category":    "Utilities",
		"payers":      []any{map[string]any{"member": 2.0, "amount": 90.0}},
		"shares": []any{
			map[string]any{"member": 1.0, "amount": 45.0},
			map[string]any{"member": 2.0, "amount": 45.0},
		},
	})

	view := runFormula(t, deps, userID, bdbID, "recurring", nil)
	if got := len(view.Result.Rows); got != 1 {
		t.Fatalf("recurring rows = %d, want 1", got)
	}
	if v := cell(t, view.Result, 0, "description"); v != "Fiber internet" {
		t.Errorf("description = %v, want the edited one", v)
	}
	if v := cell(t, view.Result, 0, "cadence"); v != "2× monthly" {
		t.Errorf("cadence = %v, want the edited interval with the unchanged frequency", v)
	}
	if v := cell(t, view.Result, 0, "total_amount"); v != 90.0 {
		t.Errorf("total_amount = %v, want re-derived from the replaced payers", v)
	}
	if v := cell(t, view.Result, 0, "category"); v != "Utilities" {
		t.Errorf("category = %v, want the typed name created and resolved", v)
	}

	// A one-sided fill must not half-break the sums-to-total invariant: no amounts move.
	runFormula(t, deps, userID, bdbID, "edit-recurring", map[string]any{
		"rowid":       float64(1),
		"description": "Fiber internet",
		"payers":      []any{map[string]any{"member": 1.0, "amount": 999.0}},
	})
	sums := execSQL(t, deps, userID, bdbID, `
		SELECT r.total_amount,
		       (SELECT SUM(rp.amount_paid) FROM recurring_payers rp WHERE rp.recurring_id = r.id) AS paid,
		       (SELECT SUM(rs.share_amount) FROM recurring_shares rs WHERE rs.recurring_id = r.id) AS owed
		FROM recurring_expenses r`, dbdomain.IntentQuery)
	if total := cell(t, sums, 0, "total_amount"); total != 90.0 {
		t.Errorf("total after one-sided edit = %v, want unchanged 90", total)
	}
	if paid, owed := cell(t, sums, 0, "paid"), cell(t, sums, 0, "owed"); paid != 90.0 || owed != 90.0 {
		t.Errorf("template sums after one-sided edit = %v/%v, want unchanged 90/90", paid, owed)
	}

	runFormula(t, deps, userID, bdbID, "apply-recurring", map[string]any{"rowid": float64(1), "date": "2026-03-01"})
	runFormula(t, deps, userID, bdbID, "stop-recurring", map[string]any{"rowid": float64(1)})

	after := runFormula(t, deps, userID, bdbID, "recurring", nil)
	if got := len(after.Result.Rows); got != 0 {
		t.Fatalf("recurring rows after stop = %d, want the bill gone from the active list", got)
	}
	kept := execSQL(t, deps, userID, bdbID,
		"SELECT (SELECT COUNT(*) FROM expenses) AS expenses, r.is_active FROM recurring_expenses r",
		dbdomain.IntentQuery)
	if v := cell(t, kept, 0, "expenses"); v != int64(1) {
		t.Errorf("expenses after stop = %v, want the materialized one kept", v)
	}
	if v := cell(t, kept, 0, "is_active"); v != int64(0) {
		t.Errorf("is_active = %v, want 0 — stopped, not deleted", v)
	}

	// The replaced templates, read the way the app shows them: name-keyed JSON on the view row.
	paidBy := moneyMap(t, cell(t, view.Result, 0, "paid_by"))
	owedBy := moneyMap(t, cell(t, view.Result, 0, "owed_by"))
	if len(paidBy) != 1 || paidBy["Bob"] != 90.0 {
		t.Errorf("paid_by = %v, want Bob fronting the whole 90", paidBy)
	}
	if len(owedBy) != 2 || owedBy["Alice"] != 45.0 || owedBy["Bob"] != 45.0 {
		t.Errorf("owed_by = %v, want the 45/45 split", owedBy)
	}
}

// TestSharedExpensesSettlementPlan pins the Balances tab's greedy plan end to end: four members with
// tangled balances get at most three suggested transfers, largest debts matched first with ids breaking
// ties, each row carrying the payer as rowid plus the seeded to_member / amount — and recording exactly
// those suggestions squares everyone, leaving the plan empty.
func TestSharedExpensesSettlementPlan(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	for _, name := range []string{"Alice", "Bob", "Chloe", "Dana"} {
		runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": name})
	}
	// Alice +90, Bob/Chloe/Dana -30 each…
	runFormula(t, deps, userID, bdbID, "add-expense", map[string]any{
		"description": "Groceries", "amount": 90.0, "custom_split": true,
		"payers": []any{map[string]any{"member": 1.0, "amount": 90.0}},
		"shares": []any{
			map[string]any{"member": 2.0, "amount": 30.0},
			map[string]any{"member": 3.0, "amount": 30.0},
			map[string]any{"member": 4.0, "amount": 30.0},
		},
	})
	// …then Bob fronts 40 consumed by Alice alone: Alice +50, Bob +10, Chloe -30, Dana -30.
	runFormula(t, deps, userID, bdbID, "add-expense", map[string]any{
		"description": "Taxi", "amount": 40.0, "custom_split": true,
		"payers": []any{map[string]any{"member": 2.0, "amount": 40.0}},
		"shares": []any{map[string]any{"member": 1.0, "amount": 40.0}},
	})

	plan := runFormula(t, deps, userID, bdbID, "balances", nil)
	want := []struct {
		payer, receiver int64
		amount          float64
		transfer        string
	}{
		{3, 1, 30.0, "Chloe pays Alice · 30.00"},
		{4, 1, 20.0, "Dana pays Alice · 20.00"},
		{4, 2, 10.0, "Dana pays Bob · 10.00"},
	}
	if got := len(plan.Result.Rows); got != len(want) {
		t.Fatalf("plan rows = %d, want %d (at most members-1 transfers)", got, len(want))
	}
	for i, w := range want {
		if v := cell(t, plan.Result, i, "transfer"); v != w.transfer {
			t.Errorf("row %d transfer = %v, want %q", i, v, w.transfer)
		}
		payer, receiver := cell(t, plan.Result, i, "rowid"), cell(t, plan.Result, i, "to_member")
		if payer != w.payer || receiver != w.receiver || cell(t, plan.Result, i, "amount") != w.amount {
			t.Errorf("row %d = %v→%v %v, want %d→%d %v for the settle-up seed",
				i, payer, receiver, cell(t, plan.Result, i, "amount"), w.payer, w.receiver, w.amount)
		}
	}

	// Recording exactly the suggestions squares everyone: net balances all zero, plan empty.
	for _, w := range want {
		runFormula(t, deps, userID, bdbID, "record-settlement", map[string]any{
			"rowid": float64(w.payer), "to_member": float64(w.receiver), "amount": w.amount,
		})
	}
	net := runFormula(t, deps, userID, bdbID, "members", nil)
	for i := range net.Result.Rows {
		if v := cell(t, net.Result, i, "balance"); v != 0.0 {
			t.Errorf("balance row %d = %v, want 0 after applying the whole plan", i, v)
		}
	}
	if got := len(runFormula(t, deps, userID, bdbID, "balances", nil).Result.Rows); got != 0 {
		t.Errorf("plan rows after settling = %d, want the all-square empty state", got)
	}
}

// transfer is one suggested payback: from pays to by amount.
type transfer struct {
	from, to int64
	amount   float64
}

// greedyPlan mirrors the balances view's matching in Go: repeatedly pair the member owing the most with
// the member owed the most (smaller id wins a tie on either side), transfer min(debt, credit), and stop
// when nobody owes or nobody is owed more than half a cent.
func greedyPlan(balances map[int64]float64) []transfer {
	transfers := []transfer{}
	remaining := maps.Clone(balances)

	for {
		mostPositive := float64(0)
		creditorID := int64(-1)

		mostNegative := float64(0)
		debtorID := int64(-1)

		for id, b := range remaining {
			if b >= 0.005 && (b > mostPositive || (b == mostPositive && id < creditorID)) {
				mostPositive = b
				creditorID = id
			}

			if b <= -0.005 && (b < mostNegative || (b == mostNegative && id < debtorID)) {
				mostNegative = b
				debtorID = id
			}
		}

		if debtorID == -1 || creditorID == -1 {
			break
		}

		amount := math.Round(100*min(mostPositive, -mostNegative)) / 100

		transfers = append(transfers, transfer{
			to:     creditorID,
			from:   debtorID,
			amount: amount,
		})

		remaining[debtorID] += amount
		remaining[creditorID] -= amount
	}

	return transfers
}

// TestSharedExpensesSettlementPlanMatchesGreedy cross-checks the SQL plan against an independent Go
// implementation of the same greedy matching, on a five-member fixture with an uneven spread and one
// member already square.
func TestSharedExpensesSettlementPlanMatchesGreedy(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	for _, name := range []string{"Ana", "Ben", "Cleo", "Dax", "Eve"} {
		runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": name})
	}
	// Ana +60, Ben +40, Cleo -50, Dax -30, Eve -20.
	runFormula(t, deps, userID, bdbID, "add-expense", map[string]any{
		"description": "Trip", "amount": 100.0, "custom_split": true,
		"payers": []any{
			map[string]any{"member": 1.0, "amount": 60.0},
			map[string]any{"member": 2.0, "amount": 40.0},
		},
		"shares": []any{
			map[string]any{"member": 3.0, "amount": 50.0},
			map[string]any{"member": 4.0, "amount": 30.0},
			map[string]any{"member": 5.0, "amount": 20.0},
		},
	})

	net := runFormula(t, deps, userID, bdbID, "members", nil)
	balances := map[int64]float64{}
	for i := range net.Result.Rows {
		id, _ := cell(t, net.Result, i, "rowid").(int64)
		amount, _ := cell(t, net.Result, i, "balance").(float64)
		balances[id] = amount
	}

	want := greedyPlan(balances)
	plan := runFormula(t, deps, userID, bdbID, "balances", nil)
	if got := len(plan.Result.Rows); got != len(want) {
		t.Fatalf("plan rows = %d, want %d from the Go greedy", got, len(want))
	}
	for i, w := range want {
		payer, receiver := cell(t, plan.Result, i, "rowid"), cell(t, plan.Result, i, "to_member")
		amount := cell(t, plan.Result, i, "amount")
		if payer != w.from || receiver != w.to || amount != w.amount {
			t.Errorf("row %d = %v→%v %v, want %d→%d %v", i, payer, receiver, amount, w.from, w.to, w.amount)
		}
	}
}

// TestSharedExpensesMemberStatus pins that toggle-member-splitting is legible from the Members tab:
// the status column names the state, a member who stops splitting sorts last while keeping their
// balance, and the toggle reads back both ways.
func TestSharedExpensesMemberStatus(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	for _, name := range []string{"Alice", "Bob"} {
		runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": name})
	}
	runFormula(t, deps, userID, bdbID, "add-expense",
		map[string]any{"description": "Dinner", "amount": 30.0, "paid_by": 1.0})

	before := runFormula(t, deps, userID, bdbID, "members", nil)
	for i := range before.Result.Rows {
		if v := cell(t, before.Result, i, "status"); v != "splitting" {
			t.Errorf("status row %d = %v, want every member splitting out of the box", i, v)
		}
	}

	// Alice — the one owed money, so first under the balance sort — stops splitting.
	runFormula(t, deps, userID, bdbID, "toggle-member-splitting", map[string]any{"rowid": float64(1)})

	out := runFormula(t, deps, userID, bdbID, "members", nil)
	if n, b := cell(t, out.Result, 0, "name"), cell(t, out.Result, 1, "name"); n != "Bob" || b != "Alice" {
		t.Errorf("names = %v/%v, want the non-splitting member sorted last (Bob, Alice)", n, b)
	}
	if v := cell(t, out.Result, 1, "status"); v != "not splitting" {
		t.Errorf("status = %v, want the toggle to read as not splitting", v)
	}
	if v := cell(t, out.Result, 1, "balance"); v != 15.0 {
		t.Errorf("balance = %v, want +15 kept — dropping out of splits settles nothing", v)
	}

	// splitting_members is what share_among's picker and its resolved "all" default both read, so a
	// non-splitter is never offered rather than picked and silently dropped. Everything that names a
	// member explicitly — settlements above all — still reads the full members table.
	opts := execSQL(t, deps, userID, bdbID,
		"SELECT group_concat(name) AS names FROM (SELECT name FROM splitting_members ORDER BY id)",
		dbdomain.IntentQuery)
	if v := cell(t, opts, 0, "names"); v != "Bob" {
		t.Errorf("splitting_members = %v, want Bob alone — Alice is out of the equal-split picker", v)
	}
	settleable := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM members WHERE id = 1", dbdomain.IntentQuery)
	if v := cell(t, settleable, 0, "n"); v != int64(1) {
		t.Errorf("Alice in members = %v, want her still nameable so her balance can be settled", v)
	}

	// Splitting again flips the same way.
	runFormula(t, deps, userID, bdbID, "toggle-member-splitting", map[string]any{"rowid": float64(1)})
	back := runFormula(t, deps, userID, bdbID, "members", nil)
	if n, v := cell(t, back.Result, 0, "name"), cell(t, back.Result, 0, "status"); n != "Alice" || v != "splitting" {
		t.Errorf("name/status = %v/%v, want Alice splitting and back on top of the balance sort", n, v)
	}
}

// TestSharedExpensesShareAmong pins add-expense's equal-split set: omitted share_among splits among
// everyone (the "all" default) with the leftover cent(s) on the payer, the resolved default covers
// only the members still splitting while an explicitly named one is honoured, a subset excluding the
// payer hands the leftover to its first listed member, an id naming nobody is rejected by the foreign
// key, and an explicit empty selection is rejected before any SQL.
func TestSharedExpensesShareAmong(t *testing.T) {
	deps, userID, bdbID := applySharedExpenses(t)

	for _, name := range []string{"Alice", "Bob", "Cara"} {
		runFormula(t, deps, userID, bdbID, "add-member", map[string]any{"name": name})
	}

	// Everyone splits 10.00 three ways; the two leftover cents land on the payer (Bob).
	runFormula(t, deps, userID, bdbID, "add-expense",
		map[string]any{"description": "Pizza", "amount": 10.0, "paid_by": 2.0})
	pizza := execSQL(t, deps, userID, bdbID, `
		SELECT (SELECT COUNT(*) FROM expense_shares es WHERE es.expense_id = e.id) AS n,
		       (SELECT share_amount FROM expense_shares WHERE expense_id = e.id AND member_id = 2) AS bob,
		       (SELECT share_amount FROM expense_shares WHERE expense_id = e.id AND member_id = 3) AS cara,
		       (SELECT SUM(share_amount) FROM expense_shares WHERE expense_id = e.id) AS sum
		FROM expenses e WHERE e.description = 'Pizza'`, dbdomain.IntentQuery)
	if n := cell(t, pizza, 0, "n"); n != int64(3) {
		t.Fatalf("Pizza share rows = %v, want the all default to cover the three members", n)
	}
	if bob, cara := cell(t, pizza, 0, "bob"), cell(t, pizza, 0, "cara"); bob != 3.34 || cara != 3.33 {
		t.Errorf("Bob/Cara shares = %v/%v, want the leftover cent on the payer (3.34/3.33)", bob, cara)
	}
	if sum := cell(t, pizza, 0, "sum"); sum != 10.0 {
		t.Errorf("Pizza share sum = %v, want the canonical total 10.00", sum)
	}

	// Cara stops splitting: a resolved "all" must skip her from now on.
	execSQL(t, deps, userID, bdbID, "UPDATE members SET is_active = 0 WHERE id = 3", dbdomain.IntentExecute)
	runFormula(t, deps, userID, bdbID, "add-expense",
		map[string]any{"description": "Dinner", "amount": 10.0, "paid_by": 1.0})
	dinner := execSQL(t, deps, userID, bdbID, `
		SELECT group_concat(member_id || ':' || share_amount) AS shares
		FROM (SELECT es.member_id, es.share_amount FROM expense_shares es
		      JOIN expenses e ON e.id = es.expense_id WHERE e.description = 'Dinner'
		      ORDER BY es.member_id)`, dbdomain.IntentQuery)
	if v := cell(t, dinner, 0, "shares"); v != "1:5.0,2:5.0" {
		t.Errorf("Dinner shares = %v, want 1:5.0,2:5.0 — the non-splitting member skipped", v)
	}

	// A subset without the payer, naming Cara past the picker (which no longer offers her): an id
	// passed explicitly is honoured, so she gets her half and the split still sums to the total.
	// Nothing is filtered at insert time — that is what keeps a pick from being silently dropped.
	runFormula(t, deps, userID, bdbID, "add-expense", map[string]any{
		"description": "Taxi", "amount": 10.0, "paid_by": 1.0, "share_among": []any{2.0, 3.0}})
	taxi := execSQL(t, deps, userID, bdbID, `
		SELECT group_concat(member_id || ':' || share_amount) AS shares,
		       (SELECT SUM(share_amount) FROM expense_shares es2 JOIN expenses e2 ON e2.id = es2.expense_id
		         WHERE e2.description = 'Taxi') AS total
		FROM (SELECT es.member_id, es.share_amount FROM expense_shares es
		      JOIN expenses e ON e.id = es.expense_id WHERE e.description = 'Taxi'
		      ORDER BY es.member_id)`, dbdomain.IntentQuery)
	if v := cell(t, taxi, 0, "shares"); v != "2:5.0,3:5.0" {
		t.Errorf("Taxi shares = %v, want 2:5.0,3:5.0 — an explicit pick is honoured, splitting or not", v)
	}
	if v := cell(t, taxi, 0, "total"); v != 10.0 {
		t.Errorf("Taxi share sum = %v, want the canonical total 10.00 — a pick is never dropped", v)
	}

	// An id naming nobody is rejected by the foreign key, not silently skipped — the alternative
	// would under-insert and leave the shares short of the total.
	_, err := deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "add-expense",
		dbservice.FormulaRunInput{Params: map[string]any{
			"description": "Ghost", "amount": 10.0, "paid_by": 1.0, "share_among": []any{2.0, 999.0}}})
	if err == nil {
		t.Fatal("unknown share_among id error = nil, want the members foreign key to reject it")
	}
	ghost := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM expenses WHERE description = 'Ghost'", dbdomain.IntentQuery)
	if v := cell(t, ghost, 0, "n"); v != int64(0) {
		t.Errorf("Ghost expenses = %v, want the rejected run rolled back whole", v)
	}

	// Unchecking everyone is a deliberate empty selection on a required param: rejected up front.
	_, err = deps.formulaSvc.RunFormula(t.Context(), userID, bdbID, "add-expense",
		dbservice.FormulaRunInput{Params: map[string]any{
			"description": "Nobody", "amount": 10.0, "paid_by": 1.0, "share_among": []any{}}})
	if err == nil || !strings.Contains(err.Error(), "share_among") {
		t.Fatalf("empty share_among error = %v, want the param named", err)
	}
	leftover := execSQL(t, deps, userID, bdbID,
		"SELECT COUNT(*) AS n FROM expenses WHERE description = 'Nobody'", dbdomain.IntentQuery)
	if v := cell(t, leftover, 0, "n"); v != int64(0) {
		t.Errorf("Nobody expenses = %v, want the rejected run to have written nothing", v)
	}
}
