package mcp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// TestTrimAffectedValues_UpdateKeepsChangedColumns verifies that an update answers with the columns whose value
// changed alone, a column logged on one side only counting as changed, and that the logged maps are left untouched.
func TestTrimAffectedValues_UpdateKeepsChangedColumns(t *testing.T) {
	oldValues := map[string]any{"id": float64(3), "title": "Plan", "tags": "[]", "done": nil, "legacy": "x"}
	newValues := map[string]any{"id": float64(3), "title": "Plan B", "tags": "[]", "done": float64(1)}

	gotOld, gotNew := trimAffectedValues(domain.ChangeOpUpdate, oldValues, newValues, 500)

	wantOld := map[string]any{"title": "Plan", "done": nil, "legacy": "x"}
	wantNew := map[string]any{"title": "Plan B", "done": float64(1)}
	if !reflect.DeepEqual(gotOld, wantOld) || !reflect.DeepEqual(gotNew, wantNew) {
		t.Errorf("trimmed = %v / %v, want %v / %v", gotOld, gotNew, wantOld, wantNew)
	}
	if len(oldValues) != 5 || len(newValues) != 4 {
		t.Errorf("logged values were modified: %v / %v", oldValues, newValues)
	}
}

// TestTrimAffectedValues_UnchangedUpdateShowsNothing verifies that an update that changed no value answers with
// neither side.
func TestTrimAffectedValues_UnchangedUpdateShowsNothing(t *testing.T) {
	oldValues := map[string]any{"id": float64(1), "title": "Plan"}
	newValues := map[string]any{"id": float64(1), "title": "Plan"}

	gotOld, gotNew := trimAffectedValues(domain.ChangeOpUpdate, oldValues, newValues, 500)

	if gotOld != nil || gotNew != nil {
		t.Errorf("trimmed = %v / %v, want nil / nil", gotOld, gotNew)
	}
}

// TestTrimAffectedValues_CutsLongTextAroundTheChange verifies that a long text changed by an update is replaced on
// both sides by a cut value giving its full length and an excerpt that holds the first changed character, while a
// short value passes whole.
func TestTrimAffectedValues_CutsLongTextAroundTheChange(t *testing.T) {
	before, after := strings.Repeat("a", 1000), strings.Repeat("z", 1000)
	oldValues := map[string]any{"body": before + "old" + after, "title": "Plan"}
	newValues := map[string]any{"body": before + "brand new" + after, "title": "Plan B"}

	gotOld, gotNew := trimAffectedValues(domain.ChangeOpUpdate, oldValues, newValues, 500)

	if gotOld["title"] != "Plan" || gotNew["title"] != "Plan B" {
		t.Errorf("titles = %v / %v, want them whole", gotOld["title"], gotNew["title"])
	}
	for side, tc := range map[string]struct {
		got        any
		wantLength int
		wantInside string
	}{
		"old": {gotOld["body"], 2003, "aold"},
		"new": {gotNew["body"], 2009, "abrand new"},
	} {
		cut, ok := tc.got.(cutValue)
		if !ok {
			t.Fatalf("%s body = %T, want a cutValue", side, tc.got)
		}
		if !cut.Truncated || cut.Length != tc.wantLength || len(cut.Excerpt) != cutValueExcerptChars ||
			!strings.Contains(cut.Excerpt, tc.wantInside) {
			t.Errorf("%s body = %+v, want length %d and a %d-character excerpt holding %q", side, cut, tc.wantLength,
				cutValueExcerptChars, tc.wantInside)
		}
		// The first changed character is at offset 1000, so position 1001: the excerpt must cover it.
		if cut.ExcerptStart > 1001 || cut.ExcerptStart+cutValueExcerptChars <= 1001 {
			t.Errorf("%s excerpt covers positions %d to %d, want it to hold position 1001", side, cut.ExcerptStart,
				cut.ExcerptStart+cutValueExcerptChars-1)
		}
	}
}

// TestTrimAffectedValues_InsertAndDeleteCutFromTheStart verifies that an inserted or deleted row keeps every column,
// with a long text cut to an excerpt that starts the value, and that characters, not bytes, are counted.
func TestTrimAffectedValues_InsertAndDeleteCutFromTheStart(t *testing.T) {
	long := "é🥖" + strings.Repeat("b", 600)
	values := map[string]any{"id": float64(7), "body": long}

	_, inserted := trimAffectedValues(domain.ChangeOpInsert, nil, values, 500)
	deleted, _ := trimAffectedValues(domain.ChangeOpDelete, values, nil, 500)

	for operation, got := range map[string]map[string]any{"insert": inserted, "delete": deleted} {
		if got["id"] != float64(7) {
			t.Errorf("%s id = %v, want 7", operation, got["id"])
		}
		cut, ok := got["body"].(cutValue)
		if !ok {
			t.Fatalf("%s body = %T, want a cutValue", operation, got["body"])
		}
		if cut.Length != 602 || cut.ExcerptStart != 1 || !strings.HasPrefix(cut.Excerpt, "é🥖bb") ||
			len([]rune(cut.Excerpt)) != cutValueExcerptChars {
			t.Errorf("%s body = %+v, want length 602 and a %d-character excerpt from position 1", operation, cut,
				cutValueExcerptChars)
		}
	}
}

// TestCutLongText verifies where the excerpt sits and that it never exceeds the limit it stands in for.
func TestCutLongText(t *testing.T) {
	text := strings.Repeat("0123456789", 100)
	for _, tc := range []struct {
		name                 string
		center, maxChars     int
		wantStart, wantChars int
	}{
		{name: "change at the start", center: 0, maxChars: 500, wantStart: 1, wantChars: 200},
		{name: "change in the middle", center: 500, maxChars: 500, wantStart: 401, wantChars: 200},
		{name: "change at the end", center: 999, maxChars: 500, wantStart: 801, wantChars: 200},
		{name: "limit under the excerpt length", center: 500, maxChars: 50, wantStart: 476, wantChars: 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cut, ok := cutLongText(text, tc.center, tc.maxChars).(cutValue)
			if !ok {
				t.Fatal("want a cutValue")
			}
			if cut.ExcerptStart != tc.wantStart || len(cut.Excerpt) != tc.wantChars || cut.Length != 1000 {
				t.Errorf("cut = start %d, %d characters, length %d; want start %d, %d characters, length 1000",
					cut.ExcerptStart, len(cut.Excerpt), cut.Length, tc.wantStart, tc.wantChars)
			}
			if want := text[cut.ExcerptStart-1 : cut.ExcerptStart-1+tc.wantChars]; cut.Excerpt != want {
				t.Errorf("excerpt = %q, want the value's characters from position %d", cut.Excerpt, cut.ExcerptStart)
			}
		})
	}

	if got := cutLongText("short", 0, 500); got != "short" {
		t.Errorf("a short text = %v, want it whole", got)
	}
	if got := cutLongText(float64(42), 0, 500); got != float64(42) {
		t.Errorf("a number = %v, want it as it is", got)
	}
	if got := cutLongText(nil, 0, 500); got != nil {
		t.Errorf("a NULL = %v, want nil", got)
	}
}

// TestCommonPrefixChars verifies that the shared prefix is counted in characters and stops at the first difference.
func TestCommonPrefixChars(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 3},
		{"abc", "abd", 2},
		{"abc", "ab", 2},
		{"café crème", "café creme", 7},
		{"🥖🥖x", "🥖🥖y", 2},
		{"x", "y", 0},
	} {
		if got := commonPrefixChars(tc.a, tc.b); got != tc.want {
			t.Errorf("commonPrefixChars(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
