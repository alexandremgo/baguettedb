package domain

import "testing"

func TestCapReached(t *testing.T) {
	tests := []struct {
		name  string
		count int
		limit int
		want  bool
	}{
		{"zero cap is unlimited", 1000, 0, false},
		{"below cap", 2, 3, false},
		{"at cap", 3, 3, true},
		{"above cap", 5, 3, true},
		{"empty at cap of zero", 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CapReached(tt.count, tt.limit); got != tt.want {
				t.Errorf("CapReached(%d, %d) = %v, want %v", tt.count, tt.limit, got, tt.want)
			}
		})
	}
}
