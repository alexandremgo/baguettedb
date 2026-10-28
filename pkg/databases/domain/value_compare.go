package domain

import (
	"encoding/json"
	"strconv"
)

// ValuesEqual reports whether a value read from a live row equals a value recorded in the change log. Live values
// are nil, int64, float64, string, or []byte; recorded values are decoded from the change log's JSON as nil,
// json.Number, string, or bool. Numbers compare by value across representations, bool compares as INTEGER 0/1,
// []byte compares against the bytes of a string, and a storage-class mismatch (TEXT "1" against the number 1) is
// not equal.
func ValuesEqual(live, recorded any) bool {
	switch l := live.(type) {
	case nil:
		return recorded == nil
	case int64:
		if n, ok := recorded.(json.Number); ok {
			nInt, err := n.Int64()
			if err == nil {
				return nInt == l
			}

			nFloat, err := n.Float64()
			if err == nil {
				return nFloat == float64(l)
			}
			return false
		}

		if b, ok := recorded.(bool); ok {
			return (l == 1 && b) || (l == 0 && !b)
		}

		return false

	case float64:
		if n, ok := recorded.(json.Number); ok {
			nFloat, err := n.Float64()
			if err == nil {
				return real15(nFloat) == real15(l)
			}
			return false
		}

		return false

	case string:
		if s, ok := recorded.(string); ok {
			return s == l
		}
		return false

	case []byte:
		if s, ok := recorded.(string); ok {
			return s == string(l)
		}
		return false

	default:
		return false
	}
}

// real15 formats f with 15 significant digits, the precision SQLite uses when rendering a REAL into JSON.
func real15(f float64) string {
	return strconv.FormatFloat(f, 'g', 15, 64)
}
