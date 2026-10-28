package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// bdbPackage is the command whose linked modules the notices cover: the binary the releases ship.
const bdbPackage = "./cmd/bdb"

// releasePlatforms are the GOOS/GOARCH pairs bdb is released for. Their module sets differ (some modules are
// linked on one operating system only), so the notices cover their union.
var releasePlatforms = [][2]string{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "amd64"},
	{"darwin", "arm64"},
}

// licenseFileName matches the names of files that carry license terms or notices: LICENSE, LICENCE, COPYING, NOTICE
// and UNLICENSE, with any suffix (LICENSE.txt, LICENSE-3RD-PARTY.md, LICENSE.libyaml, NOTICE).
var licenseFileName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|unlicense)([-._].*)?$`)

// sourceExtensions are the extensions of code files, which can share those names (notice.go, notice_test.go) without
// being license files.
var sourceExtensions = []string{".c", ".cjs", ".go", ".h", ".js", ".mjs", ".py", ".s", ".ts"}

// isLicenseFile reports whether a file named name carries license terms or notices.
func isLicenseFile(name string) bool {
	return licenseFileName.MatchString(name) && !slices.Contains(sourceExtensions, strings.ToLower(filepath.Ext(name)))
}

// listedModule is the part of `go list -json` output read here; Replace is set when go.mod replaces the module.
type listedModule struct {
	Path    string
	Version string
	Dir     string
	Main    bool
	Replace *listedModule
}

// goEntries returns the Go standard library followed by every module linked into bdb on a release platform,
// sorted by module path, with the license files found at each module's root.
func goEntries() ([]entry, []string) {
	var problems []string

	std, err := goStdEntry()
	if err != nil {
		problems = append(problems, err.Error())
	}

	modules, err := linkedModules()
	if err != nil {
		return nil, append(problems, err.Error())
	}
	entries := []entry{std}
	for _, m := range modules {
		e, errs := moduleEntry(m.Path, m.Version, m.Dir)
		entries = append(entries, e)
		problems = append(problems, errs...)
	}
	return entries, problems
}

// goStdEntry returns the entry of the Go standard library and runtime, compiled into every Go binary. It carries no
// version: toolchains a patch release apart produce the same notice.
func goStdEntry() (entry, error) {
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return entry{}, fmt.Errorf("go env GOROOT: %w", err)
	}
	text, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(goroot)), "LICENSE"))
	if err != nil {
		return entry{}, fmt.Errorf("reading the Go license: %w", err)
	}
	return entry{
		name:  "Go standard library and runtime",
		note:  "Compiled into every Go binary, bdb included.",
		files: []licenseFile{{name: "LICENSE", path: "go/LICENSE", text: normalize(string(text))}},
	}, nil
}

// linkedModules lists the modules, other than this repository's, that bdb links on any release platform, sorted
// by path. A module linked at two different versions on two platforms is an error, since one notice would then
// misstate what one of the binaries contains.
func linkedModules() ([]listedModule, error) {
	byPath := map[string]listedModule{}
	for _, p := range releasePlatforms {
		modules, err := listModules(p[0], p[1])
		if err != nil {
			return nil, err
		}
		for _, m := range modules {
			if seen, ok := byPath[m.Path]; ok && seen.Version != m.Version {
				return nil, fmt.Errorf("%s is linked at %s and %s depending on the platform", m.Path, seen.Version,
					m.Version)
			}
			byPath[m.Path] = m
		}
	}

	modules := make([]listedModule, 0, len(byPath))
	for _, m := range byPath {
		modules = append(modules, m)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	return modules, nil
}

// listModules runs `go list -deps` on bdb for one platform and returns the modules its packages come from, with
// replacements applied. CGo stays enabled, as in release builds, so the CGo-only packages are listed too.
func listModules(goos, goarch string) ([]listedModule, error) {
	cmd := exec.Command("go", "list", "-deps", "-json=Module", bdbPackage)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list for %s/%s: %w: %s", goos, goarch, err, stderr.String())
	}

	var modules []listedModule
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg struct{ Module *listedModule }
		if err := dec.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decoding go list output for %s/%s: %w", goos, goarch, err)
		}
		m := pkg.Module
		if m == nil || m.Main {
			continue // a standard library package, or one of this repository's
		}
		if m.Replace != nil {
			m = &listedModule{Path: m.Path, Version: m.Replace.Version, Dir: m.Replace.Dir}
		}
		modules = append(modules, *m)
	}
	return modules, nil
}

// moduleEntry returns the entry of the module at dir with every license file at its root, in name order. A module
// without any license file (NOTICE files aside) is a problem: no license means no permission to redistribute.
func moduleEntry(path, version, dir string) (entry, []string) {
	e := entry{name: path, version: version}
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return e, []string{fmt.Sprintf("%s: reading the module directory: %v", path, err)}
	}

	hasLicense := false
	for _, d := range dirEntries {
		if !d.Type().IsRegular() || !isLicenseFile(d.Name()) {
			continue
		}
		text, err := os.ReadFile(filepath.Join(dir, d.Name()))
		if err != nil {
			return e, []string{fmt.Sprintf("%s: reading %s: %v", path, d.Name(), err)}
		}
		f := licenseFile{name: d.Name(), path: path + "/" + d.Name(), text: normalize(string(text))}
		f.notice = isNoticeFile(f.name)
		hasLicense = hasLicense || !f.notice
		e.files = append(e.files, f)
	}
	if !hasLicense {
		return e, []string{fmt.Sprintf("%s %s: no license file at the module root", path, version)}
	}
	return e, nil
}

// isNoticeFile reports whether a file carries attribution notices (Apache-2.0's NOTICE) rather than license terms.
func isNoticeFile(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "notice")
}
