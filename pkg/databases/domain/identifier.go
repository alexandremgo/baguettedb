package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// IdentifierMaxLen caps the length, in bytes, of a user-creatable schema identifier (table, column, index,
// view, and trigger names).
const IdentifierMaxLen = 128

// identifierPattern matches user-creatable schema identifiers: an ASCII letter, then ASCII letters, digits,
// or underscores.
var identifierPattern = regexp.MustCompile("^[A-Za-z][A-Za-z0-9_]*$")

// ValidateIdentifier checks that name is a valid user-creatable schema identifier: it must start with an
// ASCII letter and contain only ASCII letters, digits, and underscores, with at most IdentifierMaxLen bytes.
//
// Reserved SQL keywords are allowed — generated SQL always quotes identifiers. The leading-letter requirement
// also excludes the platform-reserved `_` prefix.
//
// Returns an error wrapping ErrUnsupportedStatement that names the violated rule.
func ValidateIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("%w: identifier must not be empty", ErrUnsupportedStatement)
	}
	if len(name) > IdentifierMaxLen {
		return fmt.Errorf("%w: identifier %q must be at most %d bytes",
			ErrUnsupportedStatement, name, IdentifierMaxLen)
	}
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("%w: identifier %q must match %s", ErrUnsupportedStatement, name, identifierPattern.String())
	}

	return nil
}

// QuoteIdentifier wraps a SQL identifier in double quotes, doubling any embedded double quote, so reserved
// words and special characters are safe to splice into generated SQL.
func QuoteIdentifier(name string) string {
	return fmt.Sprintf(`"%s"`, strings.ReplaceAll(name, `"`, `""`))
}

// escapeTextLiteral doubles embedded single quotes so a text value is safe to splice inside a single-quoted
// SQL string literal.
func escapeTextLiteral(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
