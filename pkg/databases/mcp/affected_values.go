package mcp

import (
	"maps"
	"reflect"
	"unicode/utf8"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// defaultAffectedValueMaxChars is the longest text value, in characters, an affected row carries whole when the
// server's options leave the limit unset.
const defaultAffectedValueMaxChars = 500

// cutValueExcerptChars is the length, in characters, of the excerpt a cut value keeps.
const cutValueExcerptChars = 200

// cutValue stands in an affected row for a text value longer than the answer carries whole. A stored value is never
// a JSON object, so an object in old_values or new_values always is one of these.
type cutValue struct {
	// Truncated is always true: it marks the object as a cut value.
	Truncated bool `json:"truncated"`
	// Length is the length of the whole value, in characters.
	Length int `json:"length"`
	// Excerpt is a passage of the value: around the first character that changed when the row was updated, from
	// the start of the value otherwise.
	Excerpt string `json:"excerpt"`
	// ExcerptStart is the position of the excerpt's first character in the value, counted from 1 as SQL's substr()
	// counts.
	ExcerptStart int `json:"excerpt_start"`
}

// trimAffectedValues returns the old and new values an affected row answers with, shortened from the logged ones.
//
// For an update, only the columns whose value changed are kept: a column absent from one side counts as changed.
//
// In every operation, a text value longer than maxChars characters is replaced by a cutValue. Its excerpt is centred
// on the first character that differs between the old and the new text when both are text, and starts the value
// otherwise.
//
// The maps passed in are left untouched; a side with nothing left to show comes back nil.
func trimAffectedValues(operation string, oldValues, newValues map[string]any, maxChars int) (
	trimmedOld, trimmedNew map[string]any,
) {
	trimmedOld, trimmedNew = maps.Clone(oldValues), maps.Clone(newValues)
	if operation == domain.ChangeOpUpdate {
		for column, oldValue := range oldValues {
			if newValue, ok := newValues[column]; ok && reflect.DeepEqual(oldValue, newValue) {
				delete(trimmedOld, column)
				delete(trimmedNew, column)
			}
		}
	}

	for column, oldValue := range trimmedOld {
		newValue, inNew := trimmedNew[column]
		// The position of the change only serves to place an excerpt, so it is looked for when a side gets cut.
		center := 0
		if isLongText(oldValue, maxChars) || isLongText(newValue, maxChars) {
			oldText, oldIsText := oldValue.(string)
			newText, newIsText := newValue.(string)
			if oldIsText && newIsText {
				center = commonPrefixChars(oldText, newText)
			}
		}
		trimmedOld[column] = cutLongText(oldValue, center, maxChars)
		if inNew {
			trimmedNew[column] = cutLongText(newValue, center, maxChars)
		}
	}
	for column, newValue := range trimmedNew {
		// A column logged on the new side only (every column of an insert) has no old text to compare with: its
		// excerpt starts the value. The other columns were cut together with their old value above.
		if _, ok := trimmedOld[column]; !ok {
			trimmedNew[column] = cutLongText(newValue, 0, maxChars)
		}
	}

	if len(trimmedOld) == 0 {
		trimmedOld = nil
	}
	if len(trimmedNew) == 0 {
		trimmedNew = nil
	}
	return trimmedOld, trimmedNew
}

// cutLongText returns v as it is unless it is a text longer than maxChars characters, which it replaces by a
// cutValue whose excerpt is centred on the character at offset center (counted from 0).
func cutLongText(v any, center, maxChars int) any {
	if !isLongText(v, maxChars) {
		return v
	}
	chars := []rune(v.(string))
	// An excerpt never exceeds the limit it stands in for.
	excerptChars := max(min(cutValueExcerptChars, maxChars), 0)
	// Keeps the excerpt inside the value: it ends with the value when the change is near the end, and starts with it
	// when the change is near the start.
	start := max(min(center-excerptChars/2, len(chars)-excerptChars), 0)
	end := start + excerptChars
	return cutValue{Truncated: true, Length: len(chars), Excerpt: string(chars[start:end]), ExcerptStart: start + 1}
}

// isLongText reports whether v is a text longer than maxChars characters.
func isLongText(v any, maxChars int) bool {
	text, ok := v.(string)
	// A text has at least as many bytes as characters, so one within the limit in bytes needs no counting.
	return ok && len(text) > maxChars && utf8.RuneCountInString(text) > maxChars
}

// commonPrefixChars returns how many leading characters a and b share: the offset, counted from 0, of the first
// character that differs.
func commonPrefixChars(a, b string) int {
	count := 0
	for len(a) > 0 && len(b) > 0 {
		charA, sizeA := utf8.DecodeRuneInString(a)
		charB, sizeB := utf8.DecodeRuneInString(b)
		if charA != charB || sizeA != sizeB {
			return count
		}
		a, b = a[sizeA:], b[sizeB:]
		count++
	}
	return count
}
