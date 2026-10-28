package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// viteLicensePath is where the web build writes the license metadata of every bundled package (build.license in
// web/vite.config.ts), relative to the web app directory.
const viteLicensePath = "dist/.vite/license.json"

// workboxMarker matches the module markers the Workbox runtime leaves in the service worker files it is bundled
// into ("workbox:precaching:7.4.0"). The version in a marker is the release line, not always the exact package
// version, so only the module name is read from it.
var workboxMarker = regexp.MustCompile(`workbox:([a-z-]+):[0-9]`)

// webEntries returns the packages bundled into the web app, as Vite lists them, followed by the Workbox packages
// bundled into its service worker; both sorted by name.
func webEntries(webDir string) ([]entry, []string) {
	bundled, problems := viteEntries(webDir)
	workbox, errs := workboxEntries(webDir)
	return append(bundled, workbox...), append(problems, errs...)
}

// viteEntries reads the license metadata Vite writes next to the build. Vite reads a package's license text from
// its LICENSE file; a package bundled without one is a problem.
func viteEntries(webDir string) ([]entry, []string) {
	path := filepath.Join(webDir, viteLicensePath)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("reading %s (build the web app first): %v", path, err)}
	}
	var packages []struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		Identifier string `json:"identifier"`
		Text       string `json:"text"`
	}
	if err := json.Unmarshal(raw, &packages); err != nil {
		return nil, []string{fmt.Sprintf("decoding %s: %v", path, err)}
	}

	var entries []entry
	var problems []string
	for _, p := range packages {
		e := entry{name: p.Name, version: p.Version, declared: p.Identifier}
		if text := normalize(p.Text); text != "" {
			e.files = []licenseFile{{name: "LICENSE", path: "npm:" + p.Name + "/LICENSE", text: text}}
		} else {
			problems = append(problems, fmt.Sprintf("%s %s: bundled without a license text", p.Name, p.Version))
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries, problems
}

// workboxEntries returns the Workbox packages whose markers appear in the generated service worker files, with the
// version, declared license and LICENSE file of the package installed in node_modules, the copy the build bundles.
func workboxEntries(webDir string) ([]entry, []string) {
	files, err := filepath.Glob(filepath.Join(webDir, "dist", "workbox-*.js"))
	if err != nil {
		return nil, []string{fmt.Sprintf("listing the service worker files: %v", err)}
	}
	files = append(files, filepath.Join(webDir, "dist", "sw.js"))

	modules := map[string]bool{}
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			return nil, []string{fmt.Sprintf("reading %s: %v", f, err)}
		}
		for _, m := range workboxMarker.FindAllStringSubmatch(string(content), -1) {
			modules["workbox-"+m[1]] = true
		}
	}

	var entries []entry
	var problems []string
	for name := range modules {
		e, err := installedPackageEntry(webDir, name)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		e.note = "Bundled into the service worker."
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries, problems
}

// installedPackageEntry returns the entry of the npm package installed at node_modules/name, with the version and
// license its package.json declares and its LICENSE file.
func installedPackageEntry(webDir, name string) (entry, error) {
	dir := filepath.Join(webDir, "node_modules", name)
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return entry{}, fmt.Errorf("%s: reading its package.json (run npm ci): %w", name, err)
	}
	var manifest struct {
		Version string `json:"version"`
		License string `json:"license"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return entry{}, fmt.Errorf("%s: decoding its package.json: %w", name, err)
	}

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return entry{}, fmt.Errorf("%s: reading its directory: %w", name, err)
	}
	e := entry{name: name, version: manifest.Version, declared: manifest.License}
	for _, d := range dirEntries {
		if !d.Type().IsRegular() || !isLicenseFile(d.Name()) {
			continue
		}
		text, err := os.ReadFile(filepath.Join(dir, d.Name()))
		if err != nil {
			return entry{}, fmt.Errorf("%s: reading %s: %w", name, d.Name(), err)
		}
		e.files = append(e.files, licenseFile{
			name:   d.Name(),
			path:   "npm:" + name + "/" + d.Name(),
			text:   normalize(string(text)),
			notice: isNoticeFile(d.Name()),
		})
	}
	if len(e.files) == 0 {
		return entry{}, fmt.Errorf("%s %s: no license file in the package", name, strings.TrimSpace(manifest.Version))
	}
	return e, nil
}
