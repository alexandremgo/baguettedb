package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/google/licensecheck"
)

// allowedLicenses are the licenses whose every obligation the notices file meets by itself: permissive licenses
// whose only redistribution duty is to reproduce the copyright, the license text and any NOTICE file.
//
// Everything else fails the run, to be decided case by case: file-level copyleft (MPL, EPL, CDDL) also requires
// offering the source of the covered files, LGPL brings relinking terms that static binaries trip over, GPL and
// AGPL would extend to the whole binary, source-available or non-commercial terms restrict redistribution, and
// CC0-1.0 waives copyright but explicitly grants no patent rights.
var allowedLicenses = map[string]bool{
	"0BSD":         true,
	"Apache-2.0":   true,
	"BSD-2-Clause": true,
	"BSD-3-Clause": true,
	"BSL-1.0":      true,
	"ISC":          true,
	"MIT":          true,
	"MIT-0":        true,
	"OFL-1.1":      true,
	"Unlicense":    true,
	"Zlib":         true,
}

// minCoverage is the share of a license file's words, in percent, the classifier must recognize as license text.
// Below it, the file says more than the licenses it matched, and that remainder needs a human read: an override.
const minCoverage = 75

// override records how a person reviewed a file the classifier cannot fully recognize. It pins the file's content:
// once the text changes upstream, the override stops applying and the file fails the check until it is reviewed
// again.
type override struct {
	// license is the license expression the file was reviewed as, printed as is. Empty leaves the file out of the
	// notices, for a file about something the binaries do not contain, or one restating licenses that other files
	// already bring into the notices.
	license string

	sha256 string // hex SHA-256 of the normalized text that was reviewed
	reason string
}

// overrides are keyed by licenseFile.path. Each one covers one file, never a whole module.
var overrides = map[string]override{
	"third-party/musl/COPYRIGHT": {
		license: "MIT",
		sha256:  "0bde53fa8463607dabc042781006df1f3dc8e0f738ccef65e66619559aec6c97",
		reason: "musl's MIT license, followed by its authors and notes on the parts derived from other permissive " +
			"(BSD-2-Clause, public domain) works; all of it is reproduced.",
	},
	"modernc.org/libc/LICENSE-3RD-PARTY.md": {
		license: "BSD-3-Clause AND MIT",
		sha256:  "de2e7c240a45e22cd9f3e4e627cee0f64e23425dee2ab3adb89862f208deb83a",
		reason:  "Go's BSD-3-Clause license, and the MIT licenses of musl (with its authors), go-netdb and nixpkgs.",
	},
	"modernc.org/memory/LICENSE-LOGO": {
		sha256: "ce9c578c2968e316b80b180c3f5a5d70a8f32f37256c1bcf944a5de4f9f4eb59",
		reason: "Only the source of the repository's logo image, which the binaries do not contain.",
	},
	"modernc.org/sqlite/LICENSE-3RD-PARTY.md": {
		sha256: "d782121ab8bc21a30d401f333c3d769394651cd0d162f00a70b1655858378506",
		reason: "An index of the licenses across modernc.org/sqlite's module graph. Every component it lists as " +
			"linked brings its own license file into the notices, and the MPL-2.0 text it reproduces is for a module " +
			"bdb does not link.",
	},
	"modernc.org/sqlite/LICENSE-SQLITE": {
		license: "public domain",
		sha256:  "14088888f59c8feb910cb6cad319c6f5cddc751201662f3c09ac7b3d4ae04bfa",
		reason:  "SQLite's public domain dedication.",
	},
}

// checkEntry classifies every license file of e, records the results in the files and in e.license, and leaves
// out the files an override says to. It returns a problem for each file that is unrecognized, partly recognized or
// under a license outside allowedLicenses, and for a declared license expression without an allowed option.
//
// A package that declares its license (npm packages do) is judged on that declaration, and its texts only fail on
// a license that is recognized and not allowed; one they show beyond the declaration is added to e.license. A Go
// module or native library declares nothing, so each of its license files must be recognized in full.
func checkEntry(e *entry) []string {
	var problems []string
	var found []string
	kept := e.files[:0]
	for _, f := range e.files {
		if f.notice {
			kept = append(kept, f)
			continue
		}
		result := classifyFile(f)
		if result.omit {
			continue
		}
		f.license = strings.Join(result.licenses, " AND ")
		found = appendUnique(found, result.licenses...)
		if result.problem != "" && (e.declared == "" || result.disallowed) {
			problems = append(problems, fmt.Sprintf("%s: %s", f.path, result.problem))
		}
		kept = append(kept, f)
	}
	e.files = kept

	if e.declared == "" {
		e.license = strings.Join(found, " AND ")
		return problems
	}
	if !expressionAllowed(e.declared) {
		problems = append(problems, fmt.Sprintf("%s %s: declared license %q is not allowed", e.name, e.version,
			e.declared))
	}
	e.license = e.declared
	if extra := notInExpression(found, e.declared); len(extra) > 0 {
		if strings.Contains(e.declared, " OR ") {
			e.license = "(" + e.license + ")"
		}
		e.license += " AND " + strings.Join(extra, " AND ")
	}
	return problems
}

// classification is what classifyFile makes of a license file.
type classification struct {
	licenses []string
	omit     bool // an override leaves the file out

	problem    string // why the file fails the policy; empty when it passes
	disallowed bool   // the problem is a recognized license outside allowedLicenses
}

// classifyFile classifies f with the classifier, unless an override pinned to f's exact text says what it is. A
// problem message carries the text's hash, the value an override pins.
func classifyFile(f licenseFile) classification {
	sum := sha256.Sum256([]byte(f.text))
	hash := hex.EncodeToString(sum[:])
	if o, ok := overrides[f.path]; ok {
		if hash != o.sha256 {
			return classification{problem: "its text changed since its override was reviewed; review it again " +
				"and pin sha256 " + hash}
		}
		if o.license == "" {
			return classification{omit: true}
		}
		return classification{licenses: []string{o.license}}
	}

	coverage := licensecheck.Scan([]byte(f.text))
	var ids []string
	for _, m := range coverage.Match {
		ids = appendUnique(ids, m.ID)
	}
	for _, id := range ids {
		if !allowedLicenses[id] {
			return classification{licenses: ids, problem: "license " + id + " is not allowed", disallowed: true}
		}
	}
	switch {
	case len(ids) == 0:
		return classification{problem: "no license recognized; review the text and add an override with sha256 " +
			hash}
	case coverage.Percent < minCoverage:
		return classification{licenses: ids, problem: fmt.Sprintf("only %.0f%% recognized as %s; review the rest "+
			"and add an override with sha256 %s", coverage.Percent, strings.Join(ids, ", "), hash)}
	}
	return classification{licenses: ids}
}

// notInExpression returns the licenses of ids that are not a term of the SPDX expression expr.
func notInExpression(ids []string, expr string) []string {
	terms := strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(expr))
	var extra []string
	for _, id := range ids {
		if !slices.Contains(terms, id) {
			extra = append(extra, id)
		}
	}
	return extra
}

// expressionAllowed reports whether an SPDX license expression leaves an allowed option: every term of an AND,
// and at least one of an OR. Parentheses are ignored, so a mixed expression is read as a sum of products ("A OR B
// AND C" as A, or B and C), the SPDX precedence.
func expressionAllowed(expr string) bool {
	expr = strings.NewReplacer("(", " ", ")", " ").Replace(expr)
	for option := range strings.SplitSeq(expr, " OR ") {
		allowed := true
		for term := range strings.SplitSeq(option, " AND ") {
			allowed = allowed && allowedLicenses[strings.TrimSpace(term)]
		}
		if allowed {
			return true
		}
	}
	return false
}

// appendUnique appends each value of values not already in list.
func appendUnique(list []string, values ...string) []string {
	for _, v := range values {
		if !slices.Contains(list, v) {
			list = append(list, v)
		}
	}
	return list
}
