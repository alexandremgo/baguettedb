package httputil

import (
	"encoding/json"
	"errors"
	"net/http"
)

// IsMaxBytesError checks whether the error is caused by the request body exceeding
// the limit set by http.MaxBytesReader.
func IsMaxBytesError(err error) bool {
	_, ok := errors.AsType[*http.MaxBytesError](err)
	return ok
}

// RespondJSON writes a JSON response with the given status code. <, > and & stay as they are rather than \u escapes:
// the response is JSON, never HTML.
func RespondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	// An encoding failure comes after the status line is written, when the client has gone or data cannot be encoded
	// (a bug a test shows): there is nothing left to answer, so the error is dropped.
	_ = enc.Encode(data)
}
