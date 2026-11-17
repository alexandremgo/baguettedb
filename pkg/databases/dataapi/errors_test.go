package dataapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// TestRespondFormulaError_UnavailableSaysWhenToRetry checks that every error answered as a 503 with the unavailable
// code carries a Retry-After header, so an app always knows how long to wait before it retries.
func TestRespondFormulaError_UnavailableSaysWhenToRetry(t *testing.T) {
	h := &handler{logger: zerolog.Nop()}
	for name, err := range map[string]error{
		"a read-only bdb":                 domain.ErrDatabaseReadOnly,
		"a bdb on a newer schema":         domain.ErrNewerPlatformSchema,
		"a wrapped newer-schema refusal":  fmt.Errorf("resolving the database: %w", domain.ErrNewerPlatformSchema),
		"a wrapped read-only bdb refusal": fmt.Errorf("running the formula: %w", domain.ErrDatabaseReadOnly),
	} {
		rec := httptest.NewRecorder()
		h.respondFormulaError(rec, authhttp.DataAPIKeyIdentity{}, err)

		var body errorResponse
		if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
			t.Fatalf("%s: decoding the body %q: %v", name, rec.Body.String(), decodeErr)
		}
		if rec.Code != http.StatusServiceUnavailable || body.Code != codeUnavailable {
			t.Errorf("%s = %d %q, want 503 %q", name, rec.Code, body.Code, codeUnavailable)
		}
		if got := rec.Header().Get("Retry-After"); got != unavailableRetryAfter {
			t.Errorf("%s: Retry-After = %q, want %q", name, got, unavailableRetryAfter)
		}
	}
}
