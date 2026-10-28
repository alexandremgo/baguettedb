package cli

import (
	"os/exec"
	"testing"
)

// TestShellQuote checks that a real POSIX shell reads each quoted word back as the original, and that safe words are
// left bare.
func TestShellQuote(t *testing.T) {
	for _, word := range []string{"/usr/local/bin/bdb", "--data-dir", "My Tools", "it's", "a'b'c", "$HOME", "", "*"} {
		quoted := shellQuote(word)
		out, err := exec.Command("/bin/sh", "-c", "printf '%s' "+quoted).Output()
		if err != nil {
			t.Fatalf("sh on %s: %v", quoted, err)
		}
		if string(out) != word {
			t.Errorf("shellQuote(%q) = %s, which the shell reads as %q", word, quoted, out)
		}
	}
	if got := shellQuote("/usr/local/bin/bdb"); got != "/usr/local/bin/bdb" {
		t.Errorf("shellQuote quoted a safe word: %s", got)
	}
}

// TestTOMLString checks the escapes a TOML basic string needs.
func TestTOMLString(t *testing.T) {
	for in, want := range map[string]string{
		`/plain/path`: `"/plain/path"`,
		`C:\bdb`:      `"C:\\bdb"`,
		`say "hi"`:    `"say \"hi\""`,
		"two\nlines":  `"two\nlines"`,
	} {
		if got := tomlString(in); got != want {
			t.Errorf("tomlString(%q) = %s, want %s", in, got, want)
		}
	}
}
