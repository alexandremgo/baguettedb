package sqlitelocal

import (
	"context"
	"runtime"
	"testing"
)

// TestCipher checks that the SQLCipher version is reported, and the crypto provider the build selects: CommonCrypto
// on macOS, OpenSSL elsewhere.
func TestCipher(t *testing.T) {
	version, provider, err := Cipher(context.Background())
	if err != nil {
		t.Fatalf("Cipher: %v", err)
	}
	want := "openssl"
	if runtime.GOOS == "darwin" {
		want = "commoncrypto"
	}
	if version == "" || provider != want {
		t.Errorf("Cipher = %q, %q; want a version and %q", version, provider, want)
	}
}
