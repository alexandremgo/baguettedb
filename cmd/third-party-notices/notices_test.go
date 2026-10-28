package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkedModule reads the fixture module under testdata/name and checks it, as run does for a linked module.
func checkedModule(t *testing.T, name string) (entry, []string) {
	t.Helper()
	e, problems := moduleEntry("example.com/"+name, "v1.0.0", "testdata/"+name)
	if len(problems) > 0 {
		return e, problems
	}
	return e, checkEntry(&e)
}

// TestCheckEntry_DisallowedLicenseFails checks that a module under a license outside the allowlist (GPL-3.0) is a
// problem, so the run fails instead of writing notices for it.
func TestCheckEntry_DisallowedLicenseFails(t *testing.T) {
	_, problems := checkedModule(t, "gpl-module")
	if len(problems) != 1 || !strings.Contains(problems[0], "GPL-3.0") ||
		!strings.Contains(problems[0], "not allowed") {
		t.Fatalf("problems = %q, want one naming GPL-3.0 as not allowed", problems)
	}
}

// TestModuleEntry_NoLicenseFileFails checks that a module without any license file at its root is a problem: no
// license means no permission to redistribute.
func TestModuleEntry_NoLicenseFileFails(t *testing.T) {
	_, problems := checkedModule(t, "unlicensed-module")
	if len(problems) != 1 || !strings.Contains(problems[0], "no license file") {
		t.Fatalf("problems = %q, want one saying there is no license file", problems)
	}
}

// TestRender_PrintsNoticeFiles checks that a module's NOTICE file is printed in full next to its license, with its
// layout normalized (CRLF line endings and trailing spaces removed), and that source files named after notices
// (notice.go, notice_test.go) are not taken for one.
func TestRender_PrintsNoticeFiles(t *testing.T) {
	e, problems := checkedModule(t, "notice-module")
	if len(problems) > 0 {
		t.Fatalf("problems = %q, want none", problems)
	}
	var names []string
	for _, f := range e.files {
		names = append(names, f.name)
	}
	if strings.Join(names, ",") != "LICENSE,NOTICE" {
		t.Errorf("license files = %q, want LICENSE and NOTICE only", names)
	}
	out := render([]section{{title: "Modules", intro: "Fixtures.", entries: []entry{e}}})

	for _, want := range []string{
		"example.com/notice-module v1.0.0\n",
		"License: MIT\n",
		"NOTICE:\n\n    Example Module\n    Copyright 2026 Example Authors\n" +
			"    This product includes software developed at Example Corp.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\r") {
		t.Error("output keeps CR characters")
	}
}

// TestIsLicenseFile checks which file names count as license or notice files: the usual names with any suffix, but
// not source files that share those names.
func TestIsLicenseFile(t *testing.T) {
	tests := map[string]bool{
		"LICENSE":              true,
		"LICENSE.txt":          true,
		"LICENSE-3RD-PARTY.md": true,
		"LICENSE.libyaml":      true,
		"licence":              true,
		"COPYING":              true,
		"NOTICE":               true,
		"UNLICENSE":            true,
		"notice.go":            false,
		"notice_test.go":       false,
		"license.js":           false,
		"README.md":            false,
		"LICENSES_README":      false,
	}
	for name, want := range tests {
		if got := isLicenseFile(name); got != want {
			t.Errorf("isLicenseFile(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestRender_SharesIdenticalTexts checks that a text two components carry word for word, whatever its layout, is
// printed once under the shared texts and referenced from both, while a text only one carries stays inline.
func TestRender_SharesIdenticalTexts(t *testing.T) {
	sections := []section{{title: "Modules", intro: "Fixtures.", entries: []entry{
		{name: "a", files: []licenseFile{{name: "LICENSE", text: "Same terms\nfor everyone", license: "MIT"}}},
		{name: "b", files: []licenseFile{{name: "LICENSE", text: "Same   terms for\neveryone", license: "MIT"}}},
		{name: "c", files: []licenseFile{{name: "LICENSE", text: "Terms of c only", license: "MIT"}}},
	}}}
	out := render(sections)

	if n := strings.Count(out, "Same terms"); n != 1 {
		t.Errorf("the shared text is printed %d times, want once:\n%s", n, out)
	}
	if n := strings.Count(out, "LICENSE: shared text [1] below."); n != 2 {
		t.Errorf("the shared text is referenced %d times, want 2:\n%s", n, out)
	}
	if !strings.Contains(out, "Used by: a, b.") || !strings.Contains(out, "    Terms of c only\n") {
		t.Errorf("output lacks the shared text's users or c's inline text:\n%s", out)
	}
}

// TestClassifyFile_OverridePinsText checks that an override applies to the exact text it was reviewed on, and
// stops applying, as a problem, once that text changes.
func TestClassifyFile_OverridePinsText(t *testing.T) {
	reviewed := "Reviewed terms the classifier does not know."
	sum := sha256.Sum256([]byte(reviewed))
	overrides["example.com/m/LICENSE"] = override{license: "MIT", sha256: hex.EncodeToString(sum[:])}
	t.Cleanup(func() { delete(overrides, "example.com/m/LICENSE") })

	got := classifyFile(licenseFile{path: "example.com/m/LICENSE", text: reviewed})
	if got.problem != "" || len(got.licenses) != 1 || got.licenses[0] != "MIT" {
		t.Errorf("reviewed text: got %+v, want MIT without a problem", got)
	}
	got = classifyFile(licenseFile{path: "example.com/m/LICENSE", text: reviewed + " New clause."})
	if !strings.Contains(got.problem, "changed since its override was reviewed") {
		t.Errorf("changed text: problem = %q, want the override to stop applying", got.problem)
	}
}

// TestReadVersion checks that a version file yields the last word of its first line, whatever the line endings,
// and that a first line without any word is an error rather than an empty version.
func TestReadVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{"name then version", "SQLCipher 4.15.0\nSQLite 3.53.0 baseline\n", "4.15.0", false},
		{"version alone, no newline", "v1.2.3", "v1.2.3", false},
		{"CRLF line endings", "SQLCipher 4.15.0\r\nSQLite 3.53.0\r\n", "4.15.0", false},
		{"blank first line", "\nSQLCipher 4.15.0\n", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "VERSION")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := readVersion(path)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("readVersion() = %q, %v; want %q, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

// TestExpressionAllowed checks the reading of SPDX expressions: every term of an AND must be allowed, one option of
// an OR is enough.
func TestExpressionAllowed(t *testing.T) {
	tests := []struct {
		expr string
		want bool
	}{
		{"MIT", true},
		{"(MIT OR GPL-3.0-only)", true},
		{"MIT AND ISC", true},
		{"MIT AND GPL-3.0-only", false},
		{"GPL-3.0-only OR AGPL-3.0-only", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := expressionAllowed(tt.expr); got != tt.want {
			t.Errorf("expressionAllowed(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}
