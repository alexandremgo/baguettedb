package domain

import "testing"

// TestAccessTokenKind_Prefix checks each kind's prefix, and that an unknown kind has none.
func TestAccessTokenKind_Prefix(t *testing.T) {
	for kind, want := range map[AccessTokenKind]string{
		AccessTokenPersonal: "bdbp_",
		AccessTokenSession:  "bdbsn_",
		"unknown":           "",
	} {
		if got := kind.Prefix(); got != want {
			t.Errorf("%q.Prefix() = %q, want %q", kind, got, want)
		}
	}
}
