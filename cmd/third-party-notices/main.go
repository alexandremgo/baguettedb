// Command third-party-notices writes THIRD_PARTY_LICENSES from what the build ships: the Go modules linked into
// bdb on every release platform, the Go standard library, the packages bundled into the web app and its service
// worker, and the native libraries the release build links. Every license is checked against an allowlist, and the
// run fails without writing anything when one is missing or not allowed. The output is committed and checked by CI,
// so the notices cannot drift from the dependencies.
//
// It runs from the repository root, after the web app is built (make notices does both).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	out := flag.String("out", "THIRD_PARTY_LICENSES", "file the notices are written to")
	web := flag.String("web", "web", "directory of the web app, built beforehand")
	flag.Parse()

	if err := run(*out, *web); err != nil {
		fmt.Fprintln(os.Stderr, "third-party-notices:", err)
		os.Exit(1)
	}
}

// run collects every section, checks each license against the allowlist, and writes the notices to out. Any
// problem found while collecting or checking is reported, all of them at once, and nothing is written.
func run(out, webDir string) error {
	var problems []string
	collect := func(entries []entry, errs []string) []entry {
		problems = append(problems, errs...)
		return entries
	}

	natives := collect(nativeEntries())
	goEntries := collect(goEntries())
	webEntries := collect(webEntries(webDir))
	sections := []section{
		{
			title: "Native libraries",
			intro: "Libraries outside the Go and npm package managers: compiled from vendored source, or linked " +
				"statically by the release build.",
			entries: natives,
		},
		{
			title: "Go standard library and modules",
			intro: "The Go standard library and runtime, and every Go module linked into bdb on the release " +
				"platforms (Linux and macOS, amd64 and arm64).",
			entries: goEntries,
		},
		{
			title: "Web app",
			intro: "The npm packages bundled into the web app that bdb serves, and the service worker runtime " +
				"generated alongside it.",
			entries: webEntries,
		},
	}

	for _, s := range sections {
		for i := range s.entries {
			problems = append(problems, checkEntry(&s.entries[i])...)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%d problem(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
	}

	if err := os.WriteFile(out, []byte(render(sections)), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	return nil
}
