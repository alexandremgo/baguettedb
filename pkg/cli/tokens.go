package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// newTokensCmd builds the tokens command group, whose commands list, create and revoke the personal access tokens of
// the account they act as, the owner's or --as's; bare, it lists them.
func newTokensCmd(flags *globalFlags, open localOpener) *cobra.Command {
	return newListGroup("tokens", "List, create and revoke personal access tokens", newTokensListCmd(flags, open),
		newTokensCreateCmd(flags, open), newTokensRevokeCmd(flags, open))
}

// newTokensListCmd builds the tokens list command, which prints the account's tokens that are not revoked, newest
// first, each with the start of its value and when it was created and last used, never the value itself.
func newTokensListCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your personal access tokens, or with --as another account's",
		Args:  usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				tokens, err := l.tokens.List(cmd.Context(), l.email)
				if err != nil {
					return withUsersHint(cmd, err)
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), toAccessTokensJSON(tokens))
				}
				if len(tokens) == 0 {
					cmd.PrintErrf("No tokens for %s. Create one with `%s tokens create <token-name>`.\n", l.email,
						commandName(cmd, flags))
					return nil
				}
				now := time.Now()
				rows := make([][]string, len(tokens))
				for i, t := range tokens {
					rows[i] = []string{t.ID.String(), t.Name, t.Prefix + "…", t.CreatedAt.Local().Format(time.DateOnly),
						lastUsedLabel(t.LastUsedAt, now), expiresLabel(t.ExpiresAt, now)}
				}
				header := []string{"ID", "NAME", "TOKEN", "CREATED", "LAST USED", "EXPIRES"}
				return writeTable(cmd.OutOrStdout(), header, rows)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the tokens as JSON")
	return cmd
}

// newTokensCreateCmd builds the tokens create command, which creates a personal access token for the account, working
// for good or for --expires-in, and prints its value, alone on stdout, the only time it is shown.
func newTokensCreateCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var expiresIn string
	cmd := &cobra.Command{
		Use:   "create <token-name> [--expires-in <duration>]",
		Short: "Create a personal access token for you, or with --as another account",
		Long: "create creates a personal access token for you, or for the account --as names, and prints its " +
			"value, alone on stdout: nothing keeps it, so copy it now. A request sends it as an " +
			"`Authorization: Bearer` header; <token-name> says what it is for, to tell it apart in tokens list. " +
			"It works until revoked, or until --expires-in has passed.",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			var lifetime time.Duration
			if expiresIn != "" {
				var err error
				if lifetime, err = domain.ParseTokenLifetime(expiresIn); err != nil {
					return usageError{err: fmt.Errorf("--expires-in %w", err)}
				}
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				plain, token, err := l.tokens.Create(cmd.Context(), l.email, args[0], lifetime)
				if err != nil {
					return withUsersHint(cmd, err)
				}
				if _, err := io.WriteString(cmd.OutOrStdout(), plain+"\n"); err != nil {
					return err
				}
				expiring := ""
				if token.ExpiresAt != nil {
					expiring = ", expiring " + token.ExpiresAt.Local().Format(expiryLayout)
				}
				cmd.PrintErrf("Created token %s (%s) for %s%s. Copy it now: it can't be shown again.\n", token.Name,
					token.ID, l.email, expiring)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&expiresIn, "expires-in", "",
		"how long the token works, in days or a Go duration: 30d, 12h, 90m (default: until revoked)")
	return cmd
}

// newTokensRevokeCmd builds the tokens revoke command, which revokes one of the account's personal access tokens;
// it stops authenticating at once.
func newTokensRevokeCmd(flags *globalFlags, open localOpener) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke a personal access token, which stops working at once",
		Args:  usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				token, err := l.tokens.Revoke(cmd.Context(), l.email, args[0])
				if errors.Is(err, domain.ErrTokenNotFound) {
					err = fmt.Errorf("%w; `%s tokens` lists them", err, commandName(cmd, flags))
					if flags.as == "" {
						err = fmt.Errorf("%w; another account's token takes --as <email>", err)
					}
					return err
				}
				if err != nil {
					return withUsersHint(cmd, err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Revoked token %s (%s)\n", token.Name, token.ID)
				return err
			})
		},
	}
	return cmd
}

// commandName returns the command line as a hint shows it: its name, followed by --as and the account when the
// command acts as one, so a command the hint names acts as the same account.
func commandName(cmd *cobra.Command, flags *globalFlags) string {
	if flags.as == "" {
		return cmd.Root().Name()
	}
	return cmd.Root().Name() + " --as " + flags.as
}

// withUsersHint returns err pointing at users list when it reports an unknown account, err as is otherwise.
func withUsersHint(cmd *cobra.Command, err error) error {
	if errors.Is(err, domain.ErrUserNotFound) {
		return fmt.Errorf("%w; `%s users` lists the accounts", err, cmd.Root().Name())
	}
	return err
}

// expiryLayout is how the tokens commands print an expiry: to the minute, since a token may last hours alone.
const expiryLayout = "2006-01-02 15:04"

// expiresLabel returns when a token expires, at, as tokens list prints it: "never" when at is nil, "expired" when at
// is not after now, the local time otherwise.
func expiresLabel(at *time.Time, now time.Time) string {
	switch {
	case at == nil:
		return "never"
	case !at.After(now):
		return "expired"
	default:
		return at.Local().Format(expiryLayout)
	}
}

// lastUsedLabel returns when a token was last used, at, as tokens list prints it relative to now: "never" when at is
// nil, "just now" under a minute ago (or in the future, from a clock set differently), then the whole minutes, hours
// or days elapsed, e.g. "1 minute ago", "5 hours ago", "3 days ago".
func lastUsedLabel(at *time.Time, now time.Time) string {
	if at == nil {
		return "never"
	}

	const oneDay = 24 * time.Hour
	d := now.Sub(*at)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return countOf(int(d/time.Minute), "minute") + " ago"
	case d < oneDay:
		return countOf(int(d/time.Hour), "hour") + " ago"
	default:
		return countOf(int(d/oneDay), "day") + " ago"
	}
}
