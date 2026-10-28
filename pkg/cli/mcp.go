package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/alexandremgo/baguettedb/pkg/cli/adapters/datadir"
	"github.com/alexandremgo/baguettedb/pkg/cli/adapters/intraprocess"
	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
	"github.com/alexandremgo/baguettedb/pkg/instance"
)

// stdioServer is what the mcp command needs from an opened instance: serving it over stdio, then releasing it.
type stdioServer interface {
	ServeStdio(ctx context.Context, stdin io.Reader, stdout io.Writer, logger zerolog.Logger) error
	Close()
}

// stdioOpener opens the local instance on dataDir to serve over stdio, acting as the account with email as, or the
// owner when as is empty.
type stdioOpener func(ctx context.Context, dataDir, as string, logger zerolog.Logger) (stdioServer, error)

// openStdio opens the local instance on dataDir, as openInstance does, to serve over stdio as the account actAs
// returns for as, every tool call refused once the directory is replaced. The caller closes it.
func openStdio(ctx context.Context, dataDir, as string, logger zerolog.Logger) (stdioServer, error) {
	guard := newDataDirGuard(dataDir, "reconnect the MCP server so it starts again on the new directory (/mcp in "+
		"Claude Code, or restart the client)")
	inst, err := openInstance(ctx, dataDir, as, logger, newerBdbOverStdio, guard)
	if err != nil {
		return nil, err
	}
	auth := intraprocess.NewAuthApp(inst.Auth.UserSvc, inst.Auth.RoleSvc, inst.Auth.TokenSvc)
	acting, _, err := actAs(ctx, inst, auth, as)
	if err != nil {
		inst.Close()
		return nil, err
	}
	return actingServer{inst: inst, account: acting}, nil
}

// actingServer serves an instance over stdio as one account.
type actingServer struct {
	inst    *instance.Instance
	account intraprocess.Account
}

// ServeStdio serves the instance's MCP gateway over stdin and stdout, every call made as the account.
func (s actingServer) ServeStdio(ctx context.Context, stdin io.Reader, stdout io.Writer, logger zerolog.Logger) error {
	return s.inst.ServeStdioAs(ctx, s.account.ID, s.account.Roles, stdin, stdout, logger)
}

// Close releases the instance.
func (s actingServer) Close() {
	s.inst.Close()
}

// newMCPCmd builds the mcp command, which serves the instance open returns to an MCP client over stdin and stdout.
func newMCPCmd(flags *globalFlags, open stdioOpener) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve MCP over stdio, as the local owner or the --as account",
		Long: "mcp serves the local instance to an MCP client over stdin and stdout, every tool call made as the " +
			"local owner, or as the account --as names. MCP clients start it themselves; register it in the client " +
			"once. Logs go to stderr, since stdout carries the protocol.",
		Args: usageArgs(noArgsSuggesting),
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger, err := flags.logger(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			dataDir, err := flags.resolveDataDir()
			if err != nil {
				return err
			}
			if err := prepareDataDir(cmd, dataDir); err != nil {
				return err
			}

			if isTerminal(cmd.InOrStdin()) {
				cmd.PrintErrf("%s mcp speaks MCP on stdin and stdout, for an MCP client to start. Register it "+
					"with `%s mcp config --client <client>`; Ctrl-D quits.\n", cmd.Root().Name(), cmd.Root().Name())
			}

			srv, err := open(cmd.Context(), dataDir, flags.as, logger)
			if err != nil {
				return withActAsHint(cmd, err)
			}
			defer srv.Close()

			return srv.ServeStdio(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), logger)
		},
	}
}

// newMCPConfigCmd builds the mcp config command, which prints what to register in an MCP client so it starts this
// binary's mcp command on the same data directory, or with --http or --url, so it reaches a bdb serve over HTTP.
// argv0 is the command line's program name, as run.
func newMCPConfigCmd(flags *globalFlags, argv0 string) *cobra.Command {
	var client, name, serveURL string
	var overHTTP bool
	cmd := &cobra.Command{
		Use: "config --client <" + strings.Join(domain.MCPClients(), "|") + "> [--http | --url <url>] " +
			"[--name <name>]",
		Short: "Print what to register in an MCP client to use this instance",
		Long: "mcp config prints the snippet or command that registers this instance in an MCP client, with the " +
			"absolute path of this binary, under the name bdb-local. It only prints: no client configuration is " +
			"edited.\n\n" +
			"--http prints instead how to reach the bdb serve running on this data directory, over HTTP with a " +
			"personal access token, under the name bdb-serve; --url does the same for a server reached at another " +
			"URL, such as one on another machine. --name registers it under another name, as two instances on one " +
			"machine need.",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case client == "":
				return usageError{err: fmt.Errorf("choose the MCP client with --client:\n%s", mcpClientList())}
			case !slices.Contains(domain.MCPClients(), client):
				return usageError{err: fmt.Errorf("unknown --client %q; choose one of:\n%s", client, mcpClientList())}
			case overHTTP && serveURL != "":
				return usageError{err: errors.New("--http and --url both say where the server is: give one")}
			}
			if name != "" {
				if err := domain.ValidateMCPServerName(name); err != nil {
					return usageError{err: fmt.Errorf("--name %w", err)}
				}
			}
			if overHTTP || serveURL != "" {
				return printMCPHTTPConfig(cmd, flags, client, name, serveURL)
			}

			path, err := executablePath(argv0)
			if err != nil {
				return fmt.Errorf("locating this binary: %w", err)
			}
			if strings.HasPrefix(path, os.TempDir()) {
				cmd.PrintErrf("Warning: %s is in a temporary directory (go run?); build or install bdb and run "+
					"mcp config from there.\n", path)
			}

			var dataDir string
			if flags.dataDir != "" || os.Getenv(dataDirEnv) != "" {
				if dataDir, err = flags.resolveDataDir(); err != nil {
					return err
				}
			}
			server := domain.NewMCPServerCommand(path, dataDir, flags.as)

			key := cmp.Or(name, domain.MCPServerKey(client, flags.as))
			snippet, err := renderMCPConfig(client, key, server, runtime.GOOS)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(cmd.OutOrStdout(), snippet); err != nil {
				return err
			}
			return pointAtServe(cmd, flags, client)
		},
	}
	f := cmd.Flags()
	f.StringVar(&client, "client", "", "the MCP client to register in: "+strings.Join(domain.MCPClients(), ", "))
	f.BoolVar(&overHTTP, "http", false, "reach the bdb serve running on this data directory, over HTTP")
	f.StringVar(&serveURL, "url", "", "reach the bdb serve at this URL, over HTTP, e.g. http://nas.local:3035")
	f.StringVar(&name, "name", "", "the name to register it under (default: bdb-local, or bdb-serve over HTTP)")
	return cmd
}

// printMCPHTTPConfig prints on cmd's stdout what to register in client, under name (its default when empty), to reach
// a bdb serve over HTTP: the one at serveURL, or when it is empty, the one running on the data directory of flags,
// which is an error when none runs.
func printMCPHTTPConfig(cmd *cobra.Command, flags *globalFlags, client, name, serveURL string) error {
	base := serveURL
	if base == "" {
		dataDir, err := flags.resolveDataDir()
		if err != nil {
			return err
		}
		running, ok, err := datadir.NewServedFile(dataDir).Current()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no %s serve is running on %s: start it with `%s serve`, or give the URL of one with "+
				"--url", cmd.Root().Name(), dataDir, cmd.Root().Name())
		}
		base = running.URL
	}
	endpoint, err := domain.MCPEndpoint(base)
	if err != nil {
		return usageError{err: fmt.Errorf("--url %w", err)}
	}

	key := cmp.Or(name, domain.MCPServeKey(client, flags.as))
	snippet, err := renderMCPHTTPConfig(client, key, endpoint, cmd.Root().Name(), flags.as, runtime.GOOS)
	if err != nil {
		return err
	}
	_, err = io.WriteString(cmd.OutOrStdout(), snippet)
	return err
}

// pointAtServe says on cmd's stderr, when a bdb serve runs on the data directory of flags, that --http prints how to
// reach it for client.
func pointAtServe(cmd *cobra.Command, flags *globalFlags, client string) error {
	dataDir, err := flags.resolveDataDir()
	if err != nil {
		return err
	}
	running, ok, err := datadir.NewServedFile(dataDir).Current()
	if err != nil || !ok {
		return err
	}
	cmd.PrintErrf("\nA %s serve runs on this data directory, at %s: `%s mcp config --client %s --http` prints how to "+
		"reach it over HTTP instead.\n", cmd.Root().Name(), running.URL, cmd.Root().Name(), client)
	return nil
}

// executablePath returns the absolute path an MCP client should start this binary from: argv0 when it names a path,
// else argv0 looked up in PATH as the shell did, else os.Executable. The first two keep a package manager's link
// (such as /opt/homebrew/bin/bdb) rather than the versioned file behind it, which an upgrade removes.
func executablePath(argv0 string) (string, error) {
	if strings.ContainsRune(argv0, filepath.Separator) {
		return filepath.Abs(argv0)
	}
	if found, err := exec.LookPath(argv0); err == nil {
		return filepath.Abs(found)
	}
	return os.Executable()
}
