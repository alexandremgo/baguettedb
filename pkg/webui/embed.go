// Package webui serves the built web frontend from the binary: the static bundle, the app-shell fallback for
// client-side routes, the runtime configuration document the frontend loads before rendering, and the security
// headers a browser document needs. The bundle is copied into dist/ at build time; when only the placeholder is
// there, the handler still serves the configuration document and reports the frontend as not embedded.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Embedded reports whether the frontend bundle is compiled into the binary, as a Handler built by New reports it.
func Embedded() bool {
	_, err := fs.Stat(distFS, "dist/index.html")
	return err == nil
}
