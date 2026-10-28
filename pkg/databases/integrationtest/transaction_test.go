//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

func TestExecuteTransaction_AllSucceed(t *testing.T) {
	client, databaseID := setupAdapterSQLiteClient(t)
	ctx := context.Background()

	// Creates a table first.
	_, err := client.Execute(ctx, databaseID, "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)", nil)
	if err != nil {
		t.Fatalf("creating table: %v", err)
	}

	// Inserts 3 rows in a transaction.
	stmts := []domain.SQLStatement{
		{SQL: "INSERT INTO items (name) VALUES (?)", Args: []domain.TypedValue{domain.TextValue("milk")}},
		{SQL: "INSERT INTO items (name) VALUES (?)", Args: []domain.TypedValue{domain.TextValue("bread")}},
		{SQL: "INSERT INTO items (name) VALUES (?)", Args: []domain.TypedValue{domain.TextValue("eggs")}},
	}

	results, err := client.ExecuteTransaction(ctx, databaseID, stmts)
	if err != nil {
		t.Fatalf("executing transaction: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	for i, r := range results {
		if r.AffectedRowCount != 1 {
			t.Errorf("stmt %d: expected affected_row_count=1, got %d", i, r.AffectedRowCount)
		}
	}

	// Verifies all 3 rows exist.
	result, err := client.Execute(ctx, databaseID, "SELECT COUNT(*) FROM items", nil)
	if err != nil {
		t.Fatalf("counting rows: %v", err)
	}

	count := result.Rows[0][0].Value
	if count != int64(3) {
		t.Errorf("expected 3 rows, got %v", count)
	}
}

func TestExecuteTransaction_RollbackOnFailure(t *testing.T) {
	client, databaseID := setupAdapterSQLiteClient(t)
	ctx := context.Background()

	// Creates a table.
	_, err := client.Execute(ctx, databaseID, "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)", nil)
	if err != nil {
		t.Fatalf("creating table: %v", err)
	}

	// Second INSERT will fail (NULL for NOT NULL column).
	stmts := []domain.SQLStatement{
		{SQL: "INSERT INTO items (name) VALUES (?)", Args: []domain.TypedValue{domain.TextValue("milk")}},
		{SQL: "INSERT INTO items (name) VALUES (?)", Args: []domain.TypedValue{domain.NullValue()}},
	}

	_, err = client.ExecuteTransaction(ctx, databaseID, stmts)
	if err == nil {
		t.Fatal("expected transaction to fail")
	}

	// Verifies no rows exist (first INSERT was rolled back).
	result, err := client.Execute(ctx, databaseID, "SELECT COUNT(*) FROM items", nil)
	if err != nil {
		t.Fatalf("counting rows: %v", err)
	}

	count := result.Rows[0][0].Value
	if count != int64(0) {
		t.Errorf("expected 0 rows after rollback, got %v", count)
	}
}

func TestExecuteTransaction_Atomicity(t *testing.T) {
	client, databaseID := setupAdapterSQLiteClient(t)
	ctx := context.Background()

	// Creates a table and inserts a row.
	_, err := client.Execute(ctx, databaseID, "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)", nil)
	if err != nil {
		t.Fatalf("creating table: %v", err)
	}

	// Inserts a row and reads it back in the same transaction.
	stmts := []domain.SQLStatement{
		{SQL: "INSERT INTO items (name) VALUES (?)", Args: []domain.TypedValue{domain.TextValue("milk")}},
		{SQL: "SELECT name FROM items WHERE id = 1"},
	}

	results, err := client.ExecuteTransaction(ctx, databaseID, stmts)
	if err != nil {
		t.Fatalf("executing transaction: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// The SELECT should see the INSERT from the same transaction.
	if len(results[1].Rows) != 1 {
		t.Fatalf("expected 1 row from SELECT, got %d", len(results[1].Rows))
	}

	name := results[1].Rows[0][0].Value
	if name != "milk" {
		t.Errorf("expected name='milk', got %v", name)
	}
}
