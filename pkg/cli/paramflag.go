package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// readParamsSpec returns the JSON object cmd's --params flag (paramsJSON) or --params-file flag (paramsFile) gives,
// and nil when neither is set. More than one of --param, --params and --params-file, and parameters that are only
// blank, are usage errors.
func readParamsSpec(cmd *cobra.Command, paramsJSON, paramsFile string) ([]byte, error) {
	f := cmd.Flags()
	modes := 0
	for _, name := range []string{"param", "params", "params-file"} {
		if f.Changed(name) {
			modes++
		}
	}
	if modes > 1 {
		return nil, usageError{err: errors.New("give the parameters with --param, --params or --params-file, " +
			"only one of them")}
	}
	switch {
	case f.Changed("params") && strings.TrimSpace(paramsJSON) == "":
		return nil, usageError{err: errors.New("the parameters are empty")}
	case f.Changed("params"):
		return []byte(paramsJSON), nil
	case f.Changed("params-file"):
		return readFileFlag(cmd.InOrStdin(), paramsFile, "parameters")
	}
	return nil, nil
}

// paramFlag collects repeated --param name=value flags into a map, as a pflag.Value.
type paramFlag map[string]string

// String returns the parameters as sorted name=value pairs separated by commas, for the flag's help and errors.
func (p paramFlag) String() string {
	pairs := make([]string, 0, len(p))
	for name, value := range p {
		pairs = append(pairs, name+"="+value)
	}
	slices.Sort(pairs)
	return strings.Join(pairs, ",")
}

// Set adds one name=value parameter from s. The name is everything before the first "=", the value everything after
// it, so a value may itself contain "="; s without "=" or with an empty name is an error, and so is a name given twice.
func (p paramFlag) Set(s string) error {
	name, value, found := strings.Cut(s, "=")
	if !found || name == "" {
		return errors.New("want name=value")
	}
	if _, already := p[name]; already {
		return fmt.Errorf("%q given twice", name)
	}

	p[name] = value
	return nil
}

// Type names the flag's value in the help.
func (p paramFlag) Type() string {
	return "name=value"
}
