package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// argHints says what a positional argument is and where to find one, by the name usage lines give it.
var argHints = map[string]string{
	"<bdb>":        "<bdb> is the bdb's name or a prefix of its ID (`%[1]s bdbs` lists them)",
	"<formula>":    "<formula> is the formula's name (`%[1]s formulas <bdb>` lists them)",
	"<email>":      "<email> is the new account's email (`%[1]s users` lists the existing ones)",
	"<token-id>":   "<token-id> is the token's ID, or the start of it (`%[1]s tokens` lists them)",
	"<token-name>": "<token-name> says what the token is for, e.g. `%[1]s tokens create laptop`",
}

// usageArgNames returns the positional arguments a usage line names after the command's own name: the required
// ones, written <x>, and the optional ones, a <x> opening a [...] or (...) group. A <x> right after a --flag is that
// flag's value, not an argument.
func usageArgNames(use string) (required, optional []string) {
	fields := strings.Fields(use)
	depth := 0
	for i := 1; i < len(fields); i++ {
		field := fields[i]
		name := strings.Trim(field, "[]().")
		isArg := strings.HasPrefix(name, "<") && strings.HasSuffix(name, ">")
		flagValue := strings.HasPrefix(strings.TrimLeft(fields[i-1], "[("), "--")
		if isArg && !flagValue && depth == 0 {
			if strings.HasPrefix(field, "[") || strings.HasPrefix(field, "(") {
				optional = append(optional, name)
			} else {
				required = append(required, name)
			}
		}
		depth += strings.Count(field, "[") + strings.Count(field, "(") - strings.Count(field, "]") -
			strings.Count(field, ")")
	}
	return required, optional
}

// argsByUsage checks args against the positional arguments cmd's usage line names, and says what is wrong in the
// terms of that line, which it quotes: which argument is missing, with what each given one was read as; that a
// subcommand came after an argument; or that there are too many. A count error on a group suggests the subcommands
// close to the first argument.
func argsByUsage(cmd *cobra.Command, args []string) error {
	required, optional := usageArgNames(cmd.Use)
	path := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
	takes := path + " takes no arguments"
	if len(required)+len(optional) > 0 {
		takes = path + " takes " + strings.Join(strings.Fields(cmd.Use)[1:], " ")
	}

	for i := 1; i < len(args); i++ {
		if sub, _, err := cmd.Find(args[i : i+1]); err == nil && sub != cmd && sub.Parent() == cmd {
			usage := strings.Join(append([]string{sub.CommandPath()}, strings.Fields(sub.Use)[1:]...), " ")
			return fmt.Errorf("%q is a subcommand: put it first, `%s`", args[i], usage)
		}
	}

	err := argCountError(cmd, args, required, optional, takes)
	if err != nil && len(args) > 0 {
		return fmt.Errorf("%w%s", err, didYouMean(cmd, args[0]))
	}
	return err
}

// argCountError returns the error for args missing a required argument or holding too many, nil when their count
// fits: takes is what the command takes, as the messages say it.
func argCountError(cmd *cobra.Command, args, required, optional []string, takes string) error {
	if len(args) < len(required) {
		missing := required[len(args):]
		var msg strings.Builder
		msg.WriteString("missing " + strings.Join(missing, " and ") + ": " + takes)
		if len(args) > 0 {
			read := make([]string, len(args))
			for i, arg := range args {
				read[i] = fmt.Sprintf("%q as %s", arg, required[i])
			}
			msg.WriteString("; " + strings.Replace(strings.Join(read, ", "), " as ", " was read as ", 1))
		}
		for _, name := range missing {
			if hint, ok := argHints[name]; ok {
				msg.WriteString("; ")
				fmt.Fprintf(&msg, hint, cmd.Root().Name())
			}
		}
		return errors.New(msg.String())
	}
	if most := len(required) + len(optional); len(args) > most {
		return fmt.Errorf("too many arguments: %s, got %d", takes, len(args))
	}
	return nil
}

// noArgsSuggesting refuses any argument, as cobra.NoArgs does, as an unknown command: unlike cobra.NoArgs, it suggests
// the subcommands whose names are close to it.
func noArgsSuggesting(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("unknown command %q for %q%s", args[0], cmd.CommandPath(), didYouMean(cmd, args[0]))
}

// didYouMean returns cobra's "Did you mean this?" list of cmd's subcommands whose names are close to typed, "" when
// none is. It applies cobra's default distance when cmd sets none, as cobra itself does only in its own argument
// check.
func didYouMean(cmd *cobra.Command, typed string) string {
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	suggestions := cmd.SuggestionsFor(typed)
	if len(suggestions) == 0 {
		return ""
	}
	return "\n\nDid you mean this?\n\t" + strings.Join(suggestions, "\n\t")
}
