package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
)

// Encoders writing values out in the syntax their reader expects: a POSIX shell command line, TOML, a JSON document,
// an aligned table.

// shellJoin returns words as a POSIX shell command line, each word quoted when it needs to be.
func shellJoin(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = shellQuote(w)
	}
	return strings.Join(quoted, " ")
}

// shellSafe holds the characters a POSIX shell reads literally in a word.
const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./-_"

// shellQuote returns s unchanged when every character of it is in shellSafe, and single-quoted otherwise: an
// embedded single quote ends the quoted part, is escaped with a backslash, and a new quoted part starts.
func shellQuote(s string) string {
	isUnsafe := func(r rune) bool { return !strings.ContainsRune(shellSafe, r) }
	if s != "" && !strings.ContainsFunc(s, isUnsafe) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// tomlEscaper escapes what a TOML basic string cannot hold as is: backslashes, double quotes and line breaks.
var tomlEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

// tomlString returns s as a TOML basic string.
func tomlString(s string) string {
	return `"` + tomlEscaper.Replace(s) + `"`
}

// tomlStrings returns ss as a TOML array of basic strings.
func tomlStrings(ss []string) string {
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = tomlString(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// writeJSON writes v to w as one indented JSON document.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// Keeps <, > and & as they are: the output is read by people and programs, never embedded in HTML.
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// tableCellEscaper replaces the characters that would break a table row or its alignment with a space.
var tableCellEscaper = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")

// writeTable writes header then rows to w as aligned columns, two spaces apart, with no padding after a row's last
// non-empty cell.
func writeTable(w io.Writer, header []string, rows [][]string) error {
	var table strings.Builder
	tw := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	for _, row := range append([][]string{header}, rows...) {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = tableCellEscaper.Replace(cell)
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for line := range strings.Lines(table.String()) {
		if _, err := io.WriteString(w, strings.TrimRight(line, " \n")+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// countOf returns n followed by noun, with an s for any count but one.
func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// tableCell returns v, a SQL value (nil, int64, float64, string or []byte), as a table shows it: NULL for nil, so it
// reads apart from an empty string, and a blob's size rather than its bytes.
func tableCell(v any) string {
	switch v := v.(type) {
	case nil:
		return "NULL"
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case string:
		return v
	case []byte:
		return fmt.Sprintf("<blob, %d bytes>", len(v))
	default:
		return fmt.Sprint(v)
	}
}
