package cli

import (
	"slices"
	"testing"
)

// TestUsageArgNames checks which positional arguments a usage line names: <x> outside brackets as required, a <x>
// opening a [...] or (...) group as optional, and a <x> after a --flag as that flag's value, not an argument.
func TestUsageArgNames(t *testing.T) {
	for _, tt := range []struct {
		use                string
		required, optional []string
	}{
		{"list", nil, nil},
		{"show <bdb> <formula>", []string{"<bdb>", "<formula>"}, nil},
		{"run <bdb> <formula> [--param name=value]...", []string{"<bdb>", "<formula>"}, nil},
		{"query <bdb> [<sql> | --file <path>]", []string{"<bdb>"}, []string{"<sql>"}},
		{"revert <bdb> (<batch> | --entry <id>)", []string{"<bdb>"}, []string{"<batch>"}},
		{"create [<name>] [--description <text>] [--starter <key>]", nil, []string{"<name>"}},
		{"export <bdb> --out <file> [--passphrase-file <file>] [--force]", []string{"<bdb>"}, nil},
		{"config --client <codex|claude>", nil, nil},
	} {
		required, optional := usageArgNames(tt.use)
		if !slices.Equal(required, tt.required) || !slices.Equal(optional, tt.optional) {
			t.Errorf("usageArgNames(%q) = %q, %q; want %q, %q", tt.use, required, optional, tt.required, tt.optional)
		}
	}
}
