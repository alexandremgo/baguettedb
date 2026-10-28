package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// passwordTries is how many passwords askNewPassword asks for before giving up, so piped answers that ran out end the
// command.
const passwordTries = 3

// Errors of a password answer askNewPassword asks again after.
var (
	errPasswordRequired = errors.New("a password is required")
	errPasswordsDiffer  = errors.New("the two passwords differ")
)

// prompter asks questions on out and reads their answers from in, one line each. A password is read without echo
// when in is a terminal, as a plain line otherwise, so a script can pipe the answers. A read waiting for its answer
// ends as soon as ctx does, with ctx's error, so Ctrl-C ends a command waiting at a question.
type prompter struct {
	ctx      context.Context
	in       *bufio.Reader
	terminal *os.File
	out      io.Writer
}

// newPrompter creates a prompter asking on out and reading from in, whose reads end when ctx does.
func newPrompter(ctx context.Context, in io.Reader, out io.Writer) *prompter {
	p := &prompter{ctx: ctx, in: bufio.NewReader(in), out: out}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		p.terminal = f
	}
	return p
}

// interactive reports whether the answers come from a terminal.
func (p *prompter) interactive() bool {
	return p.terminal != nil
}

// ask prints question with current in brackets, and returns the answer trimmed of spaces, or current for an empty one.
func (p *prompter) ask(question, current string) (string, error) {
	if _, err := fmt.Fprintf(p.out, "%s [%s]: ", question, current); err != nil {
		return "", err
	}
	answer, err := p.readLine()
	if err != nil {
		return "", err
	}
	if answer = strings.TrimSpace(answer); answer == "" {
		return current, nil
	}
	return answer, nil
}

// confirm prints question followed by [Y/n] and returns whether the answer is yes: y or yes in any case, or an empty
// answer. n or no is a no; any other answer is asked again.
func (p *prompter) confirm(question string) (bool, error) {
	for {
		if _, err := fmt.Fprintf(p.out, "%s [Y/n]: ", question); err != nil {
			return false, err
		}
		answer, err := p.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// endSessionsOnce returns a function telling whether a new password ends the web sessions of the account email, of
// which open are open. With keep, it doesn't. Otherwise it does, unless p is a terminal, the account has an open
// session and the answer to "End every web session of <email>?" is no; the question is asked the first time alone.
func endSessionsOnce(p *prompter, email string, open int, keep bool) func() (bool, error) {
	asked, end := false, true
	return func() (bool, error) {
		if keep {
			return false, nil
		}
		if open == 0 || !p.interactive() || asked {
			return end, nil
		}
		var err error
		if end, err = p.confirm(fmt.Sprintf("End every web session of %s?", email)); err != nil {
			return false, err
		}
		asked = true
		return end, nil
	}
}

// askNewPassword asks question for a password, typed twice, and passes it to use. A password missing (an empty answer
// passes "" to use when allowEmpty is set), typed differently the second time, or that use finds too weak
// (domain.ErrWeakPassword) is asked again, saying why on cmd's stderr, up to passwordTries times; the last failure is
// returned. Any other error of use is returned at once.
func (p *prompter) askNewPassword(
	cmd *cobra.Command, question string, allowEmpty bool, use func(password string) error,
) error {
	for try := 1; ; try++ {
		err := p.tryNewPassword(question, allowEmpty, use)
		if err == nil {
			return nil
		}
		retry, ok := passwordRetry(err)
		if !ok || try == passwordTries {
			return err
		}
		cmd.PrintErrln(retry + " Try again.")
	}
}

// tryNewPassword asks question for a password and, unless it is empty, asks it again, then passes it to use. A missing
// password is errPasswordRequired unless allowEmpty is set; two answers that differ are errPasswordsDiffer.
func (p *prompter) tryNewPassword(question string, allowEmpty bool, use func(password string) error) error {
	password, err := p.askPassword(question)
	if err != nil {
		return err
	}
	switch {
	case password == "" && !allowEmpty:
		return errPasswordRequired
	case password != "":
		again, err := p.askPassword("Password again")
		if err != nil {
			return err
		}
		if again != password {
			return errPasswordsDiffer
		}
	}
	return use(password)
}

// askPassword prints question and returns the answer as typed, read without echo from a terminal.
func (p *prompter) askPassword(question string) (string, error) {
	if _, err := fmt.Fprintf(p.out, "%s: ", question); err != nil {
		return "", err
	}
	if p.terminal == nil {
		return p.readLine()
	}
	fd := int(p.terminal.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return "", fmt.Errorf("reading the terminal's state: %w", err)
	}
	password, err := p.await(func() (string, error) {
		b, err := term.ReadPassword(fd)
		return string(b), err
	})
	if p.interrupted(err) {
		// The read left behind keeps the terminal from echoing what is typed: it echoes again from here.
		err = errors.Join(err, term.Restore(fd, state))
	}
	// The terminal echoed no newline after the hidden answer.
	if _, werr := fmt.Fprintln(p.out); werr != nil {
		return "", errors.Join(err, werr)
	}
	if err != nil {
		return "", fmt.Errorf("reading the password: %w", err)
	}
	return password, nil
}

// readLine returns the next line of input without its line ending; the end of the input reads as an empty line.
func (p *prompter) readLine() (string, error) {
	line, err := p.await(func() (string, error) { return p.in.ReadString('\n') })
	if p.interrupted(err) {
		return "", err
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading the answer: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// interrupted reports whether err is the error of p.ctx ending, which await returns.
func (p *prompter) interrupted(err error) bool {
	return err != nil && errors.Is(err, p.ctx.Err())
}

// await runs read in a goroutine of its own and returns what it read, or p.ctx's error as soon as p.ctx ends. A read
// blocked on stdin can't be interrupted, so it is left behind, to end with the process.
func (p *prompter) await(read func() (string, error)) (string, error) {
	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		answer, err := read()
		done <- result{answer, err}
	}()
	select {
	case r := <-done:
		return r.answer, r.err
	case <-p.ctx.Done():
		return "", p.ctx.Err()
	}
}

// passwordRetry returns the line telling why a password is asked again after err, and false when err is not about
// the password.
func passwordRetry(err error) (string, bool) {
	switch {
	case errors.Is(err, errPasswordRequired):
		return "A password is required.", true
	case errors.Is(err, errPasswordsDiffer):
		return "The two passwords differ.", true
	case errors.Is(err, domain.ErrWeakPassword):
		return "That password is too weak: use at least 8 characters.", true
	default:
		return "", false
	}
}

// readPasswordStdin returns the first line of in, the password --password-stdin passes, without its line ending, or
// ctx's error once ctx ends. An empty one is a usage error.
func readPasswordStdin(ctx context.Context, in io.Reader) (string, error) {
	password, err := newPrompter(ctx, in, io.Discard).readLine()
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", usageError{err: errors.New("--password-stdin read no password")}
	}
	return password, nil
}
