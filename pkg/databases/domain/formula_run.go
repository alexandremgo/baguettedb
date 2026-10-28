package domain

import (
	"fmt"
	"regexp"
	"strings"
)

var rowidsRe = regexp.MustCompile(`:rowids\b`)

// ValidateRowIDsPlaceholder checks that sql contains exactly one expandable :rowids token (as a
// whole word — not a prefix of a longer name like :rowidsfoo). Run alongside the schema cross-check
// when a bulk parameter is declared, so a formula that would fail expansion on every run — a
// non-colon sigil like @rowids, or a second ":rowids" inside a string literal — is rejected when
// it is saved instead.
func ValidateRowIDsPlaceholder(sql string) error {
	if n := len(rowidsRe.FindAllStringIndex(sql, -1)); n != 1 {
		return fmt.Errorf("sql must contain exactly one :rowids placeholder, found %d", n)
	}
	return nil
}

// ExpandRowIDsPlaceholder rewrites the single :rowids placeholder in sql into count
// comma-separated `?` positional placeholders, so a bulk selection can be bound as
// `rowid IN (?, ?, …)`. count must be >= 1. Other placeholders are left as-is.
func ExpandRowIDsPlaceholder(sql string, count int) (string, error) {
	if count < 1 {
		return "", fmt.Errorf("count must be >= 1, was %d", count)
	}
	if err := ValidateRowIDsPlaceholder(sql); err != nil {
		return "", err
	}

	placeholders := strings.Repeat("?, ", count-1) + "?"
	return rowidsRe.ReplaceAllString(sql, placeholders), nil
}

// ResolvePageLimit returns the effective LIMIT for a paginated formula run. requested is the
// caller's :limit value, or nil when omitted. The result is defaultLimit when requested is nil or
// not positive, maxLimit when requested exceeds it, and requested otherwise. Callers pass positive
// defaultLimit <= maxLimit (e.g. DefaultPageSize and MaxPageSize).
func ResolvePageLimit(requested *int64, defaultLimit, maxLimit int64) int64 {
	if requested == nil || *requested <= 0 {
		return defaultLimit
	}

	return min(*requested, maxLimit)
}
