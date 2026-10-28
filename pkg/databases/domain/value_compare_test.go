package domain

import (
	"encoding/json"
	"testing"
)

// TestValuesEqual pins the comparison between a live SQLite value and a value recorded in the change log: numbers
// compare by value across representations and at the change log's 15-digit REAL precision, bool counts as INTEGER
// 0/1, bytes compare against a string's bytes, and a storage-class change between TEXT and a number is not equal.
func TestValuesEqual(t *testing.T) {
	tests := []struct {
		name     string
		live     any
		recorded any
		want     bool
	}{
		{"integer vs integral number", int64(1), json.Number("1"), true},
		{"integer vs real-typed number of the same value", int64(1), json.Number("1.0"), true},
		{"real vs integral number", float64(1), json.Number("1"), true},
		{"real vs fractional number", float64(2.5), json.Number("2.5"), true},
		{"real at the change log's 15-digit precision", inexactThird(), json.Number("0.3"), true},
		{"big integer exact", int64(9007199254740993), json.Number("9007199254740993"), true},
		{"big integer off by one", int64(9007199254740993), json.Number("9007199254740992"), false},
		{"integer 1 vs bool true", int64(1), true, true},
		{"integer 0 vs bool false", int64(0), false, true},
		{"integer 1 vs bool false", int64(1), false, false},
		{"same text", "a", "a", true},
		{"different text", "a", "b", false},
		{"text vs number is a storage-class change", "1", json.Number("1"), false},
		{"bytes vs text of the same bytes", []byte("hi"), "hi", true},
		{"bytes vs different text", []byte("hi"), "ho", false},
		{"both null", nil, nil, true},
		{"null vs text", nil, "x", false},
		{"integer vs null", int64(0), nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValuesEqual(tt.live, tt.recorded); got != tt.want {
				t.Errorf("ValuesEqual(%#v, %#v) = %v, want %v", tt.live, tt.recorded, got, tt.want)
			}
		})
	}
}

// inexactThird returns 0.1 + 0.2 computed at run time (0.30000000000000004), which the change log records as 0.3
// because SQLite renders REALs into JSON with 15 significant digits.
func inexactThird() float64 {
	a := 0.1
	return a + 0.2
}
