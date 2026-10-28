package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

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
