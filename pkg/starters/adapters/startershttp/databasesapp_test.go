package startershttp

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// errResponse builds a minimal *http.Response with the given status and body, as statusError receives it.
func errResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

// TestStatusError pins the error-mapping contract for unexpected internal responses: the server's {"error": …}
// message is surfaced when the body carries one, and the status alone is reported otherwise — never a decoding
// failure, since the original HTTP status is the primary signal.
func TestStatusError(t *testing.T) {
	t.Run("surfaces the server's error message", func(t *testing.T) {
		err := statusError(errResponse(422, `{"error": "statement 1: no such table: tasks"}`))
		if err == nil {
			t.Fatal("statusError() = nil, want an error")
		}
		want := "databases service returned status 422: statement 1: no such table: tasks"
		if err.Error() != want {
			t.Fatalf("statusError() = %q, want %q", err, want)
		}
	})

	t.Run("falls back to status-only for an undecodable body", func(t *testing.T) {
		err := statusError(errResponse(502, "<html>bad gateway</html>"))
		if err == nil {
			t.Fatal("statusError() = nil, want an error")
		}
		if want := "databases service returned status 502"; err.Error() != want {
			t.Fatalf("statusError() = %q, want %q", err, want)
		}
	})

	t.Run("falls back to status-only for an empty error field", func(t *testing.T) {
		err := statusError(errResponse(500, `{"error": ""}`))
		if want := "databases service returned status 500"; err == nil || err.Error() != want {
			t.Fatalf("statusError() = %v, want %q", err, want)
		}
	})
}
