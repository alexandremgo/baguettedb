package intraprocess

import (
	"bytes"
	"encoding/json"
	"errors"
)

// errMoreThanOneValue reports JSON input holding a value after the one expected.
var errMoreThanOneValue = errors.New("more than one JSON value")

// decodeOne decodes data, which must hold exactly one JSON value, into a T: a key a struct T does not declare is an
// error, and so is anything after the value.
func decodeOne[T any](data []byte) (T, error) {
	var v T
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return v, err
	}
	if d.More() {
		return v, errMoreThanOneValue
	}
	return v, nil
}
