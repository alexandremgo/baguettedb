package cli

import "testing"

// TestHumanBytes checks sizes below a kilobyte in bytes, and larger ones in the largest unit of 1000 that keeps a
// number of at least one, with one decimal.
func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 999: "999 B", 1000: "1.0 kB", 526_123: "526.1 kB", 1_500_000: "1.5 MB", 2_000_000_000: "2.0 GB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
