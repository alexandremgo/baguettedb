package sqlitelocal

import (
	"strings"
	"testing"
)

// TestKeySpec verifies the exact encoding SQLCipher requires to recognise a raw key. The shape is
// load-bearing: anything SQLCipher does not recognise is silently treated as a passphrase and stretched with
// 256,000 PBKDF2 iterations, so a malformed spec costs latency rather than raising an error.
func TestKeySpec(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
		want string
	}{
		{
			name: "zero key",
			key:  make([]byte, KeySize),
			want: "x'" + strings.Repeat("00", KeySize) + "'",
		},
		{
			name: "lowercase hex, high bytes",
			key:  []byte{0xde, 0xad, 0xbe, 0xef},
			want: "x'deadbeef'",
		},
		{
			name: "single byte pads to two digits",
			key:  []byte{0x05},
			want: "x'05'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(keySpec(tt.key)); got != tt.want {
				t.Errorf("keySpec() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestKeySpec_LengthMatchesSQLCipherRawKeyBranch pins the length SQLCipher tests for when deciding a key is
// raw rather than a passphrase: two hex digits per key byte, plus the `x'` prefix and closing quote.
func TestKeySpec_LengthMatchesSQLCipherRawKeyBranch(t *testing.T) {
	spec := keySpec(make([]byte, KeySize))
	const want = 2*KeySize + 3
	if len(spec) != want {
		t.Errorf("len(keySpec(32-byte key)) = %d, want %d", len(spec), want)
	}
}
