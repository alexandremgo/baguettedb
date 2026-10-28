package cli

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/alexandremgo/baguettedb/pkg/cli/adapters/datadir"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// dataDirGuard refuses the calls a long-running server gets once its data directory is replaced, deleted and
// recreated or restored from a backup: the server keeps the files it opened, and would go on serving data the
// directory no longer has. The files are watched once the instance has opened them, before it serves anything.
type dataDirGuard struct {
	dataDir string
	// remedy says what to do about a replaced directory, ending the error a refused call gets.
	remedy string
	opened datadir.OpenedFiles
}

// newDataDirGuard returns a guard for dataDir whose refusals end with remedy.
func newDataDirGuard(dataDir, remedy string) *dataDirGuard {
	return &dataDirGuard{dataDir: dataDir, remedy: remedy}
}

// watch records the files paths hold now, those the instance opened; an empty path is skipped.
func (g *dataDirGuard) watch(paths ...string) error {
	return g.opened.Watch(paths...)
}

// check returns an error saying the data directory was replaced when a watched path is gone or holds another file,
// and nil otherwise.
func (g *dataDirGuard) check() error {
	path, changed := g.opened.Changed()
	if !changed {
		return nil
	}
	return fmt.Errorf("the data directory %s was replaced since this server started (%s is not the file it opened: "+
		"the directory was deleted and recreated, or restored), so it no longer serves it: %s",
		g.dataDir, filepath.Base(path), g.remedy)
}

// httpMiddleware refuses every request with 503 and check's error while the data directory is replaced.
func (g *dataDirGuard) httpMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := g.check(); err != nil {
			httputil.RespondJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// toolMiddleware answers every MCP tool call with an error result carrying check's error, instead of running the
// tool, while the data directory is replaced.
func (g *dataDirGuard) toolMiddleware(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := g.check(); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return next(ctx, req)
	}
}
