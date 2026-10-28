package main

import (
	"fmt"
	"os"
	"path"
	"strings"
)

// native describes a library no package manager knows about. Its license texts are committed under third-party/,
// copied verbatim from upstream.
type native struct {
	name string

	// versionFile, when set, is a file whose first line ends with the version ("SQLCipher 4.15.0"). Libraries
	// taken from the build system's packages have none: their version is whatever the build image ships.
	versionFile string

	note  string
	files []string
}

// natives are the libraries compiled from vendored source or linked statically by the release build.
var natives = []native{
	{
		name:        "SQLCipher (Community Edition)",
		versionFile: "pkg/databases/adapters/sqlitelocal/csrc/SQLCIPHER_VERSION",
		note: "Compiled from the vendored amalgamation into every bdb binary: the encryption layer of each bdb's " +
			"database file. It includes SQLite, which is in the public domain.",
		files: []string{"third-party/sqlcipher/LICENSE"},
	},
	{
		name: "OpenSSL (libcrypto)",
		note: "SQLCipher's cryptography on Linux, linked statically into the Linux release binaries of bdb: " +
			"OpenSSL 3 as Alpine Linux packages it when the release is built. The macOS binaries use the system's " +
			"own cryptography instead. Copyright the OpenSSL Project Authors.",
		files: []string{"third-party/openssl/LICENSE.txt"},
	},
	{
		name: "musl libc",
		note: "The C library linked statically into the Linux release binaries of bdb, as Alpine Linux packages it " +
			"when the release is built.",
		files: []string{"third-party/musl/COPYRIGHT"},
	},
}

// nativeEntries returns the entries of the native libraries, in the order they are declared.
func nativeEntries() ([]entry, []string) {
	var entries []entry
	var problems []string
	for _, n := range natives {
		e := entry{name: n.name, note: n.note}
		if n.versionFile != "" {
			version, err := readVersion(n.versionFile)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", n.name, err))
			}
			e.version = version
		}
		for _, f := range n.files {
			text, err := os.ReadFile(f)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: reading %s: %v", n.name, f, err))
				continue
			}
			e.files = append(e.files, licenseFile{name: path.Base(f), path: f, text: normalize(string(text))})
		}
		entries = append(entries, e)
	}
	return entries, problems
}

// readVersion returns the last word of the first line of the file at path.
func readVersion(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	firstLine, _, _ := strings.Cut(string(content), "\n")
	fields := strings.Fields(firstLine)
	if len(fields) == 0 {
		return "", fmt.Errorf("%s has no version on its first line", path)
	}

	return fields[len(fields)-1], nil
}
