//go:build cgo

package sqlitelocal

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// queryText runs sql on c and returns the text of the single column of its first row, or the error the statement
// raised, while being prepared or while being run.
func queryText(t *testing.T, c *Conn, sql string, args ...any) (string, error) {
	t.Helper()
	rows, err := c.Query(context.Background(), sql, args...)
	if err != nil {
		return "", err
	}
	defer tClose(t, "rows", rows)

	if !rows.Next() {
		return "", rows.Err()
	}
	var content string
	err = rows.Scan(&content)
	return content, err
}

// TestTextPatch_ReplacesTheSingleMatch verifies that text_patch returns the text with its one occurrence of old
// replaced, and raises an error naming the function when old is absent.
func TestTextPatch_ReplacesTheSingleMatch(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	got, err := queryText(t, c, "SELECT text_patch(?, ?, ?)", "one two three", "two", "2")
	if err != nil {
		t.Fatalf("text_patch: %v", err)
	}
	if want := "one 2 three"; got != want {
		t.Errorf("text_patch = %q, want %q", got, want)
	}

	_, err = queryText(t, c, "SELECT text_patch(?, ?, ?)", "one two three", "four", "4")
	if err == nil {
		t.Fatal("text_patch with an absent old: got no error")
	}
	if !strings.Contains(err.Error(), "text_patch: old not found") {
		t.Errorf("error = %q, want it to say that old was not found", err)
	}
}

// TestOpen_RegistersTextFunctions verifies that a connection opened through Open can call text_patch and
// text_replace_between.
func TestOpen_RegistersTextFunctions(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	mustExec(t, c, "SELECT text_patch('a b c', 'b', 'x')")
	mustExec(t, c, "SELECT text_replace_between('a b c d', 'b', 'c', 'x')")
}

// TestTextFunctions_RefusedInSchema verifies that nothing stored in a schema can run a text function: a CHECK
// constraint, an index expression and a generated column are refused when created, while a view, a trigger and a
// column default are refused when used.
func TestTextFunctions_RefusedInSchema(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)
	mustExec(t, c, "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)")
	mustExec(t, c, "CREATE TABLE logs (id INTEGER PRIMARY KEY, body TEXT)")

	// wantUnsafe fails the test unless err is the engine's refusal of a direct-only function.
	wantUnsafe := func(t *testing.T, err error) {
		t.Helper()
		if sqlErr, ok := errors.AsType[*Error](err); !ok || !strings.Contains(sqlErr.Msg, "unsafe use of text_") {
			t.Errorf("error = %v, want an *Error reporting an unsafe use of a text function", err)
		}
	}

	atCreation := []struct{ name, ddl string }{
		{"check constraint", "CREATE TABLE checked (body TEXT CHECK (text_patch(body, 'a', 'b') <> ''))"},
		{"check constraint with text_replace_between",
			"CREATE TABLE checked (body TEXT CHECK (text_replace_between(body, 'a', 'b', 'c') <> ''))"},
		{"index expression", "CREATE INDEX notes_patched ON notes (text_patch(body, 'a', 'b'))"},
		{"generated column",
			"CREATE TABLE generated (body TEXT, patched TEXT GENERATED ALWAYS AS (text_patch(body, 'a', 'b')))"},
	}
	for _, tc := range atCreation {
		t.Run(tc.name+" refused when created", func(t *testing.T) {
			_, err := c.Exec(context.Background(), tc.ddl)
			wantUnsafe(t, err)
		})
	}

	atUse := []struct{ name, ddl, use string }{
		{"view", "CREATE VIEW patched AS SELECT text_patch(body, 'a', 'b') AS body FROM notes",
			"SELECT body FROM patched"},
		{"trigger",
			"CREATE TRIGGER logs_patch AFTER INSERT ON logs BEGIN " +
				"UPDATE logs SET body = text_patch(body, 'a', 'b') WHERE id = NEW.id; END",
			"INSERT INTO logs (body) VALUES ('a')"},
		{"column default",
			"CREATE TABLE drafts (id INTEGER PRIMARY KEY, body TEXT DEFAULT (text_patch('a', 'a', 'b')))",
			"INSERT INTO drafts DEFAULT VALUES"},
	}
	for _, tc := range atUse {
		t.Run(tc.name+" refused when used", func(t *testing.T) {
			mustExec(t, c, tc.ddl)
			_, err := c.Exec(context.Background(), tc.use)
			wantUnsafe(t, err)
		})
	}
}

// countOccurrences returns how many times sub occurs in s, overlapping occurrences included: "aa" occurs twice in
// "aaa". sub must not be empty.
func countOccurrences(s, sub string) int {
	count := 0
	start := 0
	for {
		i := strings.Index(s[start:], sub)
		if i < 0 {
			break
		}
		start += i + 1
		count++
	}
	return count
}

// FuzzTextPatch checks text_patch against a model written in Go: the call succeeds exactly when old occurs once in
// the text, and then returns the text with that occurrence replaced.
func FuzzTextPatch(f *testing.F) {
	f.Add("a b c", "b", "x")
	f.Add("aaa", "aa", "x")
	f.Add("abc", "z", "")
	f.Add("café 🥖 crème", "🥖", "pain")
	f.Add("line one\nline two\n", "\nline", "\n- line")

	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		f.Fatalf("rand: %v", err)
	}
	c, err := Open(filepath.Join(f.TempDir(), "fuzz.db"), key, 0)
	if err != nil {
		f.Fatalf("open: %v", err)
	}
	f.Cleanup(func() {
		if err := c.Close(); err != nil {
			f.Errorf("close conn: %v", err)
		}
	})

	f.Fuzz(func(t *testing.T, text, old, repl string) {
		if old == "" {
			t.Skip("an empty old is refused before any search")
		}
		got, err := queryText(t, c, "SELECT text_patch(?, ?, ?)", text, old, repl)
		if n := countOccurrences(text, old); n != 1 {
			if err == nil {
				t.Fatalf("old occurs %d times in the text: got %q and no error", n, got)
			}
			return
		}
		if err != nil {
			t.Fatalf("old occurs once in the text: %v", err)
		}
		if want := strings.Replace(text, old, repl, 1); got != want {
			t.Errorf("text_patch(%q, %q, %q) = %q, want %q", text, old, repl, got, want)
		}
	})
}

// TestTextFunctions verifies, case by case, the text that text_patch and text_replace_between return, or the
// error they raise.
func TestTextFunctions(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	const patch = "SELECT text_patch(?, ?, ?)"
	const between = "SELECT text_replace_between(?, ?, ?, ?)"
	tests := []struct {
		name string
		sql  string
		args []any
		want string
		// wantErr is a fragment of the message of the expected *Error; empty when the call succeeds.
		wantErr string
	}{
		{name: "patch replaces the single match", sql: patch, args: []any{"a b c", "b", "x"}, want: "a x c"},
		{name: "patch deletes with an empty new", sql: patch, args: []any{"a b c", "b ", ""}, want: "a c"},
		{name: "patch replaces the whole text", sql: patch, args: []any{"abc", "abc", "x"}, want: "x"},
		{name: "patch at the start", sql: patch, args: []any{"abc", "a", "xyz"}, want: "xyzbc"},
		{name: "patch at the end", sql: patch, args: []any{"abc", "c", "xyz"}, want: "abxyz"},
		{name: "patch around multi-byte text", sql: patch, args: []any{"café 🥖 crème", "🥖", "pain"},
			want: "café pain crème"},
		{name: "patch with quotes and a line break", sql: patch, args: []any{"it's a\nline", "'s a\n", " was one "},
			want: "it was one line"},
		{name: "patch reads numbers as text", sql: patch, args: []any{1234, 23, 99}, want: "1994"},
		{name: "patch old absent", sql: patch, args: []any{"a b c", "z", "x"}, wantErr: "text_patch: old not found"},
		{name: "patch old longer than text", sql: patch, args: []any{"a", "ab", "x"}, wantErr: "old not found"},
		{name: "patch old repeated", sql: patch, args: []any{"a b b", "b", "x"}, wantErr: "old found 2 times"},
		{name: "patch old overlapping itself", sql: patch, args: []any{"aaa", "aa", "x"}, wantErr: "old found 2 times"},
		{name: "patch empty old", sql: patch, args: []any{"abc", "", "x"}, wantErr: "old is empty"},
		{name: "patch NULL text", sql: patch, args: []any{nil, "a", "x"}, wantErr: "text is NULL"},
		{name: "patch NULL old", sql: patch, args: []any{"a", nil, "x"}, wantErr: "old is NULL"},
		{name: "patch NULL new", sql: patch, args: []any{"a", "a", nil}, wantErr: "new is NULL"},
		{name: "patch with too few arguments", sql: "SELECT text_patch('a')", wantErr: "wrong number of arguments"},

		{name: "between replaces the block, anchors included", sql: between,
			args: []any{"intro START middle END outro", "START", "END", "X"}, want: "intro X outro"},
		{name: "between ignores an end before start", sql: between,
			args: []any{"END a START b END c", "START", "END", "X"}, want: "END a X c"},
		{name: "between end repeated after start", sql: between,
			args: []any{"a START b END c END", "START", "END", "X"}, wantErr: "end found 2 times after start"},
		{name: "between end absent", sql: between, args: []any{"a START b", "START", "END", "X"},
			wantErr: "text_replace_between: end not found after start"},
		{name: "between end only before start", sql: between, args: []any{"END a START b", "START", "END", "X"},
			wantErr: "end not found after start"},
		{name: "between start repeated", sql: between, args: []any{"a START START b END", "START", "END", "X"},
			wantErr: "start found 2 times"},
		{name: "between end overlapping start", sql: between, args: []any{"abc", "abc", "c", "X"},
			wantErr: "end not found after start"},
		{name: "between empty end", sql: between, args: []any{"a b", "a", "", "X"}, wantErr: "end is empty"},
		{name: "between NULL new", sql: between, args: []any{"a b", "a", "b", nil}, wantErr: "new is NULL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := queryText(t, c, tt.sql, tt.args...)
			if tt.wantErr != "" {
				if sqlErr, ok := errors.AsType[*Error](err); !ok || !strings.Contains(sqlErr.Msg, tt.wantErr) {
					t.Errorf("error = %v, want an *Error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res != tt.want {
				t.Errorf("got %q, want %q", res, tt.want)
			}
		})
	}
}
