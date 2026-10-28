package domain

import (
	"errors"
	"testing"
	"time"
)

// TestParseTokenLifetime checks that a lifetime is whole days with a d suffix or a Go duration, and that an empty, a
// zero, a negative, a fractional-day, a unitless and an overflowing one are refused.
func TestParseTokenLifetime(t *testing.T) {
	for s, want := range map[string]time.Duration{
		"30d":   30 * 24 * time.Hour,
		"1d":    24 * time.Hour,
		"12h":   12 * time.Hour,
		"90m":   90 * time.Minute,
		"1h30m": 90 * time.Minute,
	} {
		if got, err := ParseTokenLifetime(s); err != nil || got != want {
			t.Errorf("ParseTokenLifetime(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "0d", "0", "-1h", "-2d", "1.5d", "30", "d", "999999999999d"} {
		if _, err := ParseTokenLifetime(s); !errors.Is(err, ErrInvalidTokenLifetime) {
			t.Errorf("ParseTokenLifetime(%q) = %v, want ErrInvalidTokenLifetime", s, err)
		}
	}
}
