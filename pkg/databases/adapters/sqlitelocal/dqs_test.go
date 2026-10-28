//go:build cgo

package sqlitelocal

import (
	"context"
	"testing"
)

// TestDQS_DisabledOnOpen verifies that a double-quoted token that doesn't resolve to an identifier is a
// parse error — for DML and DDL alike — instead of silently becoming a string literal.
func TestDQS_DisabledOnOpen(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	ctx := context.Background()
	if _, err := c.Exec(ctx, "CREATE TABLE items (c TEXT)"); err != nil {
		t.Fatalf("seed table: %v", err)
	}

	// SQLite's two DQS switches split by schema-or-not: DQS_DDL covers CREATE/ALTER/DROP, DQS_DML covers
	// every other statement — SELECTs included, despite the name.
	if _, err := c.Exec(ctx, `SELECT "missing" FROM items`); err == nil {
		t.Error("DQS_DML side: double-quoted non-identifier in a SELECT should be a parse error, got success")
	}
	if _, err := c.Exec(ctx, `CREATE TABLE bad (c TEXT CHECK (c <> "nope"))`); err == nil {
		t.Error("DQS_DDL side: double-quoted non-identifier in a CREATE should be a parse error, got success")
	}
}

// TestDQS_LegacySchemaStillLoads verifies SQLite's stored-schema exemption in this build: a schema that
// relies on the double-quoted-string fallback still loads, serves reads, and enforces its CHECK on a
// connection where the fallback is disabled.
func TestDQS_LegacySchemaStillLoads(t *testing.T) {
	path := tmpDB(t)
	key := newKey(t)
	ctx := context.Background()

	c, err := Open(path, key, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := c.setDQS(true); err != nil {
		tClose(t, "conn", c)
		t.Fatalf("re-enable dqs: %v", err)
	}
	if _, err := c.Exec(ctx, `CREATE TABLE legacy (c TEXT CHECK (c <> "forbidden"))`); err != nil {
		tClose(t, "conn", c)
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := c.Exec(ctx, "INSERT INTO legacy (c) VALUES ('x')"); err != nil {
		tClose(t, "conn", c)
		t.Fatalf("seed legacy row: %v", err)
	}
	tClose(t, "conn", c)

	c2, err := Open(path, key, 0)
	if err != nil {
		t.Fatalf("reopen with DQS off: %v", err)
	}
	defer tClose(t, "conn", c2)

	if _, err := c2.Exec(ctx, "INSERT INTO legacy (c) VALUES ('y')"); err != nil {
		t.Errorf("insert against legacy DQS schema failed: %v", err)
	}
	if _, err := c2.Exec(ctx, "INSERT INTO legacy (c) VALUES ('forbidden')"); err == nil {
		t.Error("legacy CHECK should still treat \"forbidden\" as a string literal and reject the row")
	}
}
