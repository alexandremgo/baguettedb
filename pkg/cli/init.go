package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// newInitCmd builds the init command, which sets the owner's email, name and password, asking for each in turn.
func newInitCmd(flags *globalFlags, open localOpener) *cobra.Command {
	var email, name string
	var keepSessions bool
	cmd := &cobra.Command{
		Use:   "init [--email <email>] [--name <full-name>] [--keep-sessions]",
		Short: "Set your email, name and password, those of the instance's owner",
		Long: "init asks for the owner's email, name and password, showing the current ones: Enter keeps them. The " +
			"owner signs in to serve's web app with this email and password, and keeps their bdbs whatever the " +
			"email. --email and --name answer their question beforehand.\n\n" +
			"A new password signs the owner out of the web app everywhere: from a terminal, it asks first when a web " +
			"session is open; --keep-sessions keeps them. Personal access tokens stay.",
		Args: usageArgs(argsByUsage),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.as != "" {
				return usageError{err: errors.New("init sets the owner's own account: --as doesn't apply")}
			}
			return runLocal(cmd, flags, open, func(l *local) error {
				return initOwner(cmd, l, email, name, keepSessions)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&email, "email", "", "your email, which you sign in with")
	f.StringVar(&name, "name", "", "your full name: its first word the first name, the rest the last name")
	f.BoolVar(&keepSessions, "keep-sessions", false, "keep your web sessions open when the password changes")
	return cmd
}

// initOwner asks on cmd's stderr for the owner's email unless email is set, their name unless name is set, and a
// password, then sets them, and says who the owner now is.
//
// An empty answer keeps the current email, name or password; an owner without a password must choose one, typed
// twice, and is asked again for it as askNewPassword says. Nothing changes until a password is taken.
//
// A new password ends the owner's web sessions as endSessionsOnce says, keepSessions keeping them; the question comes
// once the password is found strong enough.
func initOwner(cmd *cobra.Command, l *local, email, name string, keepSessions bool) error {
	owner, err := l.owner.Owner(cmd.Context())
	if err != nil {
		return err
	}
	open := 0
	if owner.HasPassword && !keepSessions {
		if open, err = l.owner.OpenSessions(cmd.Context()); err != nil {
			return err
		}
	}
	p := newPrompter(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr())
	if email == "" {
		if email, err = p.ask("Email", owner.Email); err != nil {
			return err
		}
	}
	if name == "" {
		if name, err = p.ask("Name", fullName(owner)); err != nil {
			return err
		}
	}

	question := "Password (at least 8 characters)"
	if owner.HasPassword {
		question = "New password (Enter keeps the current one)"
	}
	endSessions := endSessionsOnce(p, email, open, keepSessions)
	var updated domain.User
	ended := false
	err = p.askNewPassword(cmd, question, owner.HasPassword, func(password string) (err error) {
		end := false
		if password != "" {
			if err = l.users.CheckPassword(password); err != nil {
				return err
			}
			if end, err = endSessions(); err != nil {
				return err
			}
		}
		if updated, err = l.owner.Update(cmd.Context(), email, name, password, end); err != nil {
			return err
		}
		ended = end && open > 0
		return nil
	})
	if err != nil {
		return err
	}
	if ended {
		cmd.PrintErrln("Ended your web sessions: sign in again with the new password.")
	}
	cmd.PrintErrf("You are %s <%s>, the owner: sign in to `%s serve`'s web app with this email and password.\n",
		fullName(updated), updated.Email, cmd.Root().Name())
	return nil
}
