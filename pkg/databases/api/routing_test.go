package api

import "testing"

// TestBdbIDFromInternalBody pins the body peek for the bdb-scoped internal endpoints: a body carrying a
// valid bdb_id yields that bdb (so InternalBodyProxy routes to the owner); a body without one (e.g. the
// pod-local snapshot endpoints) or a malformed body yields ok=false so the request is served locally.
func TestBdbIDFromInternalBody(t *testing.T) {
	const bdb = "22222222-2222-2222-2222-222222222222"

	id, ok := bdbIDFromInternalBody([]byte(`{"user_id":"u","bdb_id":"` + bdb + `"}`))
	if !ok || id.String() != bdb {
		t.Fatalf("bdbIDFromInternalBody(valid) = (%v, %v), want (%s, true)", id, ok, bdb)
	}

	noRoute := map[string]string{
		"no bdb_id":    `{"database_id":"some-db"}`,
		"empty bdb":    `{"user_id":"u","bdb_id":""}`,
		"invalid uuid": `{"bdb_id":"not-a-uuid"}`,
		"empty object": `{}`,
		"malformed":    `{not json`,
		"empty body":   ``,
	}
	for name, raw := range noRoute {
		if id, ok := bdbIDFromInternalBody([]byte(raw)); ok {
			t.Errorf("bdbIDFromInternalBody(%s) = (%v, true), want ok=false", name, id)
		}
	}
}

// TestOrdinalFromPodName pins the contract of the StatefulSet-ordinal parser: the integer after the final '-',
// rejecting names with no '-' or a non-numeric / negative suffix.
func TestOrdinalFromPodName(t *testing.T) {
	valid := []struct {
		name string
		want int
	}{
		{"databases-0", 0},
		{"databases-2", 2},
		{"databases-10", 10},
		{"my-svc-3", 3},      // service name itself contains a '-': split on the LAST one
		{"databases-007", 7}, // leading zeros are fine
		{"databases--1", 1},  // suffix after the LAST '-' is "1"; no leading dash survives, so never negative
	}
	for _, tc := range valid {
		got, err := OrdinalFromPodName(tc.name)
		if err != nil {
			t.Errorf("OrdinalFromPodName(%q) unexpected error: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("OrdinalFromPodName(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}

	invalid := []string{
		"",            // empty
		"databases",   // no '-'
		"databases-",  // empty suffix
		"databases-x", // non-numeric suffix
		"databases--", // empty suffix after last '-'
	}
	for _, name := range invalid {
		if _, err := OrdinalFromPodName(name); err == nil {
			t.Errorf("OrdinalFromPodName(%q) = nil error, want an error", name)
		}
	}
}

// TestBuildPeerURL pins the peer-URL template substitution: {color} maps to the fleet name (0->blue, 1->green)
// and {ordinal} to the pod ordinal, with each token substituted everywhere it appears.
func TestBuildPeerURL(t *testing.T) {
	const tmpl = "http://databases-{color}-{ordinal}.databases-{color}:8002"
	cases := []struct {
		color, ordinal int16
		want           string
	}{
		{0, 0, "http://databases-blue-0.databases-blue:8002"},
		{1, 0, "http://databases-green-0.databases-green:8002"},
		{1, 3, "http://databases-green-3.databases-green:8002"},
	}
	for _, tc := range cases {
		if got := BuildPeerURL(tmpl, tc.color, tc.ordinal); got != tc.want {
			t.Errorf("BuildPeerURL(%q, %d, %d) = %q, want %q", tmpl, tc.color, tc.ordinal, got, tc.want)
		}
	}
}
