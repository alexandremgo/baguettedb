package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/alexandremgo/baguettedb/pkg/cli/adapters/datadir"
	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// defaultListen is the address bdb serve listens on without --listen: this machine only.
const defaultListen = "127.0.0.1:3035"

// shutdownTimeout bounds how long a stopping server waits for requests in flight before closing their connections.
const shutdownTimeout = 5 * time.Second

// readHeaderTimeout bounds how long the server waits for a request's headers, so a client can't hold a connection
// open by sending them slowly.
const readHeaderTimeout = 10 * time.Second

// newServeCmd builds the serve command, which serves the local instance over HTTP until interrupted: its API, the
// streamable MCP endpoint and the web app, every request authenticated with a personal access token or the session of
// a browser signed in with a password. While it runs, it is recorded in the data directory, so a second server on the
// same data directory is refused.
//
// An owner without a password can't sign in to the web app: started from a terminal, serve first asks the init
// questions, opening the instance with open; otherwise its start lines say to run init.
func newServeCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var listen, publicURL string
	var allowedHosts []string
	cmd := &cobra.Command{
		Use:   "serve [--listen <address>] [--url <url>] [--allow-host <host>]...",
		Short: "Serve the local instance over HTTP: its API and MCP, with personal access tokens",
		Long: "serve serves the local instance over HTTP until Ctrl-C: its API under /api, MCP (streamable HTTP) at " +
			"/mcp, and the web app. Scripts and MCP clients send a personal access token as an `Authorization: " +
			"Bearer` header (tokens create <token-name>); people sign in to the web app with their email and " +
			"password (init for yours, users set-password for the others'). It listens on " + defaultListen + ", " +
			"this machine only; --listen 0.0.0.0:3035 serves the network too, over plain HTTP, and --url says the " +
			"URL others reach it at. A request naming the server by anything but localhost or an IP address is " +
			"refused, against DNS rebinding, unless --allow-host names it.",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.as != "" {
				return usageError{err: errors.New("serve acts as whoever each request's token or session belongs " +
					"to: --as doesn't apply")}
			}
			if publicURL != "" {
				if err := checkServeURL(publicURL); err != nil {
					return usageError{err: fmt.Errorf("invalid --url %q: %w", publicURL, err)}
				}
			}
			logger, err := flags.logger(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			dataDir, err := flags.resolveDataDir()
			if err != nil {
				return err
			}
			// A second server would replace the first one's record, and the first to stop would remove it.
			served := datadir.NewServedFile(dataDir)
			if running, ok, err := served.Current(); err != nil {
				return err
			} else if ok {
				return fmt.Errorf("%s serve is already serving %s at %s (process %d): stop it first, or serve "+
					"another data directory with --data-dir", cmd.Root().Name(), dataDir, running.URL, running.PID)
			}

			owner, err := ensureOwnerPassword(cmd, flags, open)
			if err != nil {
				return err
			}

			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("listening on %s: %w", listen, err)
			}
			baseURL := strings.TrimRight(publicURL, "/")
			if baseURL == "" {
				baseURL = listenURL(ln.Addr())
			}
			guard := newDataDirGuard(dataDir, fmt.Sprintf("stop `%s serve` and start it again", cmd.Root().Name()))
			inst, err := openServedInstance(cmd.Context(), dataDir, logger, baseURL, allowedHosts, guard)
			if err != nil {
				return errors.Join(err, ln.Close())
			}
			defer inst.Close()

			record := domain.ServedInstance{URL: baseURL, PID: os.Getpid()}
			if err := served.Record(record); err != nil {
				return errors.Join(err, ln.Close())
			}
			defer func() {
				if err := served.Forget(os.Getpid()); err != nil {
					logger.Warn().Err(err).Msg("could not forget the served instance")
				}
			}()

			err = printServing(cmd.ErrOrStderr(), cmd.Root().Name(), dataDir, baseURL, inst.WebAppEmbedded, owner)
			if err != nil {
				return errors.Join(err, ln.Close())
			}
			if addr, ok := ln.Addr().(*net.TCPAddr); ok && !addr.IP.IsLoopback() {
				cmd.PrintErrf("Warning: %s is reachable beyond this machine, over plain HTTP: tokens and data cross "+
					"the network unencrypted. Put a TLS proxy in front, or listen on 127.0.0.1.\n",
					listenedAs(listen, addr))
			}

			srv := &http.Server{Handler: inst.Router, ReadHeaderTimeout: readHeaderTimeout}
			return serveUntilDone(cmd.Context(), srv, ln)
		},
	}
	f := cmd.Flags()
	f.StringVar(&listen, "listen", defaultListen, "the address to listen on, host:port (port 0 picks a free one)")
	f.StringVar(&publicURL, "url", "",
		"the URL browsers and MCP clients reach the server at, e.g. http://nas.local:3035 (default: from --listen)")
	f.StringArrayVar(&allowedHosts, "allow-host", nil,
		"a name the server may be reached by besides localhost and IP addresses, e.g. nas.local (repeatable)")
	return cmd
}

// printServing writes to w the lines bdb serve, named name, starts with: the data directory it serves at baseURL, how
// owner signs in to the web app, and how scripts and MCP clients authenticate. Without webApp, the binary has no web
// app, which the lines say instead, with how to build one that has it; an owner without a password is told to run
// init.
func printServing(w io.Writer, name, dataDir, baseURL string, webApp bool, owner domain.User) error {
	lines := fmt.Sprintf("Serving %s on %s (MCP at %s/mcp). Ctrl-C stops.\n", dataDir, baseURL, baseURL)
	switch {
	case !webApp:
		lines += "This build has no web app, so there is no browser sign-in: build it with `make build-cli-ui` " +
			"for one.\n"
	case owner.HasPassword:
		lines += fmt.Sprintf("Sign in at %s as %s, with your password.\n", baseURL, owner.Email)
	default:
		lines += fmt.Sprintf("The owner, %s, has no password yet to sign in to the web app with: run `%s init`.\n",
			owner.Email, name)
	}
	lines += fmt.Sprintf("Scripts and MCP clients send a personal access token as their bearer token: "+
		"`%s tokens create <token-name>`.\n", name)
	_, err := io.WriteString(w, lines)
	return err
}

// checkServeURL returns an error unless raw is an absolute http or https URL with a host.
func checkServeURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("want scheme http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("no host")
	}
	return nil
}

// ensureOwnerPassword opens the local instance with open and returns its owner, once the init questions are answered
// when the owner has no password and cmd's stdin is a terminal.
func ensureOwnerPassword(cmd *cobra.Command, flags *globalFlags, open localOpener) (domain.User, error) {
	var owner domain.User
	err := runLocal(cmd, flags, open, func(l *local) error {
		var err error
		if owner, err = l.owner.Owner(cmd.Context()); err != nil || owner.HasPassword {
			return err
		}
		if !newPrompter(cmd.Context(), cmd.InOrStdin(), io.Discard).interactive() {
			return nil
		}
		cmd.PrintErrf("You, the owner (%s), have no password yet to sign in to the web app with. Set it now:\n",
			owner.Email)
		if err := initOwner(cmd, l, "", "", false); err != nil {
			return err
		}
		owner, err = l.owner.Owner(cmd.Context())
		return err
	})
	return owner, err
}

// listenedAs returns the address listen, as the --listen flag gave it, with addr's port: the port picked for port 0,
// and the host as typed, where addr would show [::] for 0.0.0.0.
func listenedAs(listen string, addr *net.TCPAddr) string {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return addr.String()
	}
	return net.JoinHostPort(host, strconv.Itoa(addr.Port))
}

// listenURL returns the URL of a server listening on addr: http, with 127.0.0.1 in place of an address listening on
// every interface, which no browser can open.
func listenURL(addr net.Addr) string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || !tcp.IP.IsUnspecified() {
		return "http://" + addr.String()
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port))
}

// serveUntilDone serves srv on ln until ctx is done, then shuts it down, giving requests in flight shutdownTimeout to
// finish before closing their connections. A stop through ctx returns nil; a server that fails returns its error.
func serveUntilDone(ctx context.Context, srv *http.Server, ln net.Listener) error {
	served := make(chan error, 1)
	go func() {
		served <- srv.Serve(ln)
	}()

	select {
	case err := <-served:
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); errors.Is(err, context.DeadlineExceeded) {
		// Requests still in flight, such as an MCP client's open event stream, are cut off.
		if err := srv.Close(); err != nil {
			return fmt.Errorf("closing the server's connections: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stopping the server: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving: %w", err)
	}
	return nil
}
