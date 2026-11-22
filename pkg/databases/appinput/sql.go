package appinput

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// maxExactFloatInteger is the largest whole number a JSON number decoded as a float64 still carries exactly (2^53).
const maxExactFloatInteger = 1 << 53

// SQLParams converts the values of a request's SQL parameters, keyed by placeholder name without its sigil ("x" for
// ":x"), into the values to bind. Each value binds as JSON types it: a string as text, a whole number as an integer,
// another number as a real, true and false as 1 and 0, null as NULL, and an array or object as its JSON text.
//
// A number may arrive as a float64 or as a json.Number. A whole float64 beyond 2^53 binds as a real, since it no
// longer holds its exact integer.
//
// An empty name, and a name written with its sigil, are errors.
func SQLParams(params map[string]any) (map[string]domain.TypedValue, error) {
	values := make(map[string]domain.TypedValue, len(params))
	for name, raw := range params {
		if name == "" {
			return nil, errors.New("a parameter has an empty name")
		}
		if strings.ContainsRune(":@$?", rune(name[0])) {
			return nil, fmt.Errorf("parameter %q: write the name without its %q, as %q", name, name[:1], name[1:])
		}
		value, err := sqlParamValue(raw)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		values[name] = value
	}
	return values, nil
}

// sqlParamValue converts one decoded JSON value into the value to bind.
func sqlParamValue(raw any) (domain.TypedValue, error) {
	switch v := raw.(type) {
	case nil:
		return domain.NullValue(), nil
	case string:
		return domain.TextValue(v), nil
	case bool:
		if v {
			return domain.IntegerValue(1), nil
		}
		return domain.IntegerValue(0), nil
	case float64:
		if v == math.Trunc(v) && math.Abs(v) <= maxExactFloatInteger {
			return domain.IntegerValue(int64(v)), nil
		}
		return domain.RealValue(v), nil
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return domain.IntegerValue(n), nil
		}
		f, err := v.Float64()
		if err != nil {
			return domain.TypedValue{}, fmt.Errorf("%s is not a number SQLite can hold", v)
		}
		return domain.RealValue(f), nil
	case map[string]any, []any:
		text, err := jsonText(v)
		if err != nil {
			return domain.TypedValue{}, err
		}
		return domain.TextValue(text), nil
	default:
		return domain.TypedValue{}, fmt.Errorf("unsupported value of type %T", raw)
	}
}

// jsonText returns v as compact JSON text, keeping "<", ">" and "&" as they are.
func jsonText(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
