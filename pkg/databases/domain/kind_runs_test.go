package domain

import (
	"slices"
	"testing"
)

// TestKindRuns pins the run splitting a mixed batch reverses by: consecutive entries of one kind form a run, the runs
// keep the entries' order, and a consumer that stops early stops the iteration.
func TestKindRuns(t *testing.T) {
	ddl := func(id int64) *ChangeLogEntry { return &ChangeLogEntry{ID: id, Kind: ChangeKindDDL} }
	dml := func(id int64) *ChangeLogEntry { return &ChangeLogEntry{ID: id, Kind: ChangeKindDML} }
	ids := func(run []*ChangeLogEntry) []int64 {
		out := make([]int64, 0, len(run))
		for _, e := range run {
			out = append(out, e.ID)
		}
		return out
	}

	tests := []struct {
		name    string
		entries []*ChangeLogEntry
		want    [][]int64
	}{
		{"empty", nil, nil},
		{"DDL only", []*ChangeLogEntry{ddl(3), ddl(2)}, [][]int64{{3, 2}}},
		{"DML only", []*ChangeLogEntry{dml(5), dml(4), dml(3)}, [][]int64{{5, 4, 3}}},
		{"drop with cascaded rows", []*ChangeLogEntry{ddl(9), dml(8), dml(7)}, [][]int64{{9}, {8, 7}}},
		{"alternating", []*ChangeLogEntry{ddl(4), dml(3), ddl(2), dml(1)}, [][]int64{{4}, {3}, {2}, {1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got [][]int64
			for run := range KindRuns(tt.entries) {
				got = append(got, ids(run))
			}
			if !slices.EqualFunc(got, tt.want, slices.Equal) {
				t.Errorf("runs = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("stops when the consumer does", func(t *testing.T) {
		yielded := 0
		for range KindRuns([]*ChangeLogEntry{ddl(3), dml(2), ddl(1)}) {
			yielded++
			break
		}
		if yielded != 1 {
			t.Errorf("runs yielded after a break = %d, want 1", yielded)
		}
	})
}
