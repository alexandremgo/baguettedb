package domain

import "strings"

// CheckConstraintsFromCreateSQL extracts every CHECK constraint expression from a stored CREATE
// statement, in declaration order, covering both column-level and table-level (named or anonymous)
// constraints. SQLite exposes no pragma for CHECK constraints, so the statement text kept in
// sqlite_master is the only introspection source. The scan honors string literals and quoted
// identifiers, so a CHECK inside a literal or a quoted name never matches, and the parenthesis
// balancing honors them too. Returns nil when the statement declares no CHECK.
func CheckConstraintsFromCreateSQL(createSQL string) []string {
	var checks []string
	for i := 0; i < len(createSQL); i++ {
		switch c := createSQL[i]; c {
		case '\'', '"', '`':
			i = skipQuoted(createSQL, i, c)
		case '[':
			i = skipQuoted(createSQL, i, ']')
		default:
			if !matchesKeywordAt(createSQL, i, "check") {
				continue
			}
			j := i + len("check")
			for j < len(createSQL) && isSQLSpace(createSQL[j]) {
				j++
			}
			if j >= len(createSQL) || createSQL[j] != '(' {
				i = j - 1
				continue
			}
			expr, end, ok := balancedParenBody(createSQL, j)
			if !ok {
				return checks
			}
			checks = append(checks, strings.TrimSpace(expr))
			i = end
		}
	}
	return checks
}

// skipQuoted returns the index of the closing delimiter for the quoted region opening at start, or
// the last index of s when the region never closes. A doubled delimiter inside a '\” or '"' region
// is the SQL escape for a literal delimiter and does not close it.
func skipQuoted(s string, start int, closer byte) int {
	for i := start + 1; i < len(s); i++ {
		if s[i] != closer {
			continue
		}
		if (closer == '\'' || closer == '"') && i+1 < len(s) && s[i+1] == closer {
			i++
			continue
		}
		return i
	}
	return len(s) - 1
}

// matchesKeywordAt reports whether the lowercase keyword occurs at index i of s, case-insensitively
// and on word boundaries, so a column or literal merely containing the keyword never matches.
func matchesKeywordAt(s string, i int, keyword string) bool {
	if i+len(keyword) > len(s) {
		return false
	}
	if !strings.EqualFold(s[i:i+len(keyword)], keyword) {
		return false
	}
	if i > 0 && isWordChar(s[i-1]) {
		return false
	}
	return i+len(keyword) == len(s) || !isWordChar(s[i+len(keyword)])
}

// balancedParenBody returns the text between the parenthesis opening at open and its balanced
// closing counterpart, plus the closing index. Nesting and quoted regions are honored; ok is false
// when the parenthesis never closes.
func balancedParenBody(s string, open int) (body string, end int, ok bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'', '"', '`':
			i = skipQuoted(s, i, c)
		case '[':
			i = skipQuoted(s, i, ']')
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], i, true
			}
		}
	}
	return "", len(s), false
}

func isSQLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func isWordChar(c byte) bool {
	return c == '_' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}
