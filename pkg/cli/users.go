package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// newUsersCmd builds the users command group, whose commands list and create the instance's accounts and set their
// passwords; bare, it lists them.
func newUsersCmd(flags *globalFlags, open localOpener) *cobra.Command {
	return newListGroup("users", "List and create the accounts of this instance, and set their passwords",
		newUsersListCmd(flags, open), newUsersCreateCmd(flags, open), newUsersSetPasswordCmd(flags, open))
}

// newUsersListCmd builds the users list command, which prints every account with its name, role and creation date.
func newUsersListCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the accounts",
		Args:  usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				users, err := l.users.List(cmd.Context())
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), toUsersJSON(users))
				}
				rows := make([][]string, len(users))
				for i, u := range users {
					rows[i] = []string{u.Email, fullName(u), roleOf(u), u.CreatedAt.Local().Format(time.DateOnly)}
				}
				return writeTable(cmd.OutOrStdout(), []string{"EMAIL", "NAME", "ROLE", "CREATED"}, rows)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the accounts as JSON")
	return cmd
}

// newUsersCreateCmd builds the users create command, which creates an account, with a password asked in a terminal or
// read with --password-stdin, and prints it.
func newUsersCreateCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var name string
	var admin, passwordStdin, asJSON bool
	cmd := &cobra.Command{
		Use:   "create <email> [--name <full-name>] [--admin] [--password-stdin]",
		Short: "Create an account, which signs in to serve's web app with its password",
		Long: "create creates an account for <email>. From a terminal, it asks for the password the account signs in " +
			"to serve's web app with (Enter for none); --password-stdin reads it from the first line of stdin " +
			"instead. Without either, the account has no password and signs in with the personal access tokens " +
			"created for it (--as <email> tokens create <token-name>), until users set-password gives it one. " +
			"--name is the person's full name, its first word the first name and the rest the last name; --admin " +
			"makes the account an admin of the instance.",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocal(cmd, flags, open, func(l *local) error {
				user, err := createUser(cmd, l, args[0], name, admin, passwordStdin)
				if errors.Is(err, domain.ErrUserExists) {
					return fmt.Errorf("%w; `%s users` lists the accounts", err, cmd.Root().Name())
				}
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), toUserJSON(user))
				}
				line := fmt.Sprintf("Created %s (%s)", user.Email, user.ID)
				if user.Admin {
					line += ", an admin"
				}
				if _, err := io.WriteString(cmd.OutOrStdout(), line+"\n"); err != nil {
					return err
				}
				if user.HasPassword {
					cmd.PrintErrf("It signs in to `%s serve`'s web app with this email and its password.\n",
						cmd.Root().Name())
				} else {
					cmd.PrintErrf("It has no password: create a token it signs in with, `%[1]s --as %[2]s tokens "+
						"create <token-name>`, or give it a password, `%[1]s users set-password %[2]s`.\n",
						cmd.Root().Name(), user.Email)
				}
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "the person's full name: its first word is the first name, the rest the last name")
	f.BoolVar(&admin, "admin", false, "make the account an admin of the instance")
	f.BoolVar(&passwordStdin, "password-stdin", false, "read the account's password from the first line of stdin")
	f.BoolVar(&asJSON, "json", false, "print the new account as JSON")
	return cmd
}

// createUser creates the account email, named name, an admin one when admin is set, with the password read from
// cmd's stdin when passwordStdin is set, asked when stdin is a terminal (Enter for none), and none otherwise.
func createUser(cmd *cobra.Command, l *local, email, name string, admin, passwordStdin bool) (domain.User, error) {
	if passwordStdin {
		password, err := readPasswordStdin(cmd.Context(), cmd.InOrStdin())
		if err != nil {
			return domain.User{}, err
		}
		return l.users.Create(cmd.Context(), email, name, password, admin)
	}
	p := newPrompter(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr())
	if !p.interactive() {
		return l.users.Create(cmd.Context(), email, name, "", admin)
	}
	var user domain.User
	err := p.askNewPassword(cmd, "Password (at least 8 characters; Enter for none, tokens only)", true,
		func(password string) error {
			var err error
			user, err = l.users.Create(cmd.Context(), email, name, password, admin)
			return err
		})
	return user, err
}

// newUsersSetPasswordCmd builds the users set-password command, which sets the password an account signs in to
// serve's web app with, asked in a terminal or read with --password-stdin, and ends the account's web sessions.
func newUsersSetPasswordCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var passwordStdin, keepSessions bool
	cmd := &cobra.Command{
		Use:   "set-password <email> [--password-stdin] [--keep-sessions]",
		Short: "Set the password an account signs in to serve's web app with, replacing any",
		Long: "set-password sets the password of the account <email>, whatever its current one: from a terminal, it " +
			"asks for it twice; --password-stdin reads it from the first line of stdin instead. It also resets a " +
			"forgotten password, the owner's included.\n\n" +
			"The new password signs the account out of the web app everywhere, so a leaked password stops working " +
			"for whoever had it: from a terminal, it asks first when the account has a web session open; " +
			"--keep-sessions keeps them. Personal access tokens stay.",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			p := newPrompter(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr())
			if !passwordStdin && !p.interactive() {
				return usageError{err: errors.New("no terminal to ask the password in: pass it with --password-stdin")}
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				ended, err := setPassword(cmd, l, p, email, passwordStdin, keepSessions)
				if err != nil {
					return withUsersHint(cmd, err)
				}
				if ended {
					cmd.PrintErrf("Set the password of %s, and ended its web sessions.\n", email)
				} else {
					cmd.PrintErrf("Set the password of %s.\n", email)
				}
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&passwordStdin, "password-stdin", false, "read the password from the first line of stdin")
	f.BoolVar(&keepSessions, "keep-sessions", false, "keep the account's web sessions open")
	return cmd
}

// setPassword sets the password of the account email, read from cmd's stdin when passwordStdin is set and asked with
// p otherwise, ending its web sessions as endSessionsOnce says, once the password is found strong enough. It reports
// whether it ended at least one.
func setPassword(
	cmd *cobra.Command, l *local, p *prompter, email string, passwordStdin, keepSessions bool,
) (bool, error) {
	open := 0
	if !keepSessions {
		var err error
		if open, err = l.users.OpenSessions(cmd.Context(), email); err != nil {
			return false, err
		}
	}
	endSessions := endSessionsOnce(p, email, open, keepSessions)
	ended := false
	use := func(password string) error {
		if err := l.users.CheckPassword(password); err != nil {
			return err
		}
		end, err := endSessions()
		if err != nil {
			return err
		}
		if err := l.users.SetPassword(cmd.Context(), email, password, end); err != nil {
			return err
		}
		ended = end && open > 0
		return nil
	}

	var err error
	if passwordStdin {
		var password string
		if password, err = readPasswordStdin(cmd.Context(), cmd.InOrStdin()); err == nil {
			err = use(password)
		}
	} else {
		err = p.askNewPassword(cmd, "New password (at least 8 characters)", false, use)
	}
	return ended, err
}

// fullName returns u's first and last names, separated by a space when both are set.
func fullName(u domain.User) string {
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

// roleOf returns u's role as users list prints it: admin or user.
func roleOf(u domain.User) string {
	if u.Admin {
		return "admin"
	}
	return "user"
}
