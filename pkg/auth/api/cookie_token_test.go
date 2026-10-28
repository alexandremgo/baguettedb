package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

// mockHydraPublic stands in for the authorization server's token endpoints, recording what it is asked.
type mockHydraPublic struct {
	exchangeResp *domain.TokenSet
	exchangeErr  error
	revokeErr    error

	// lastExchange captures the request passed to ExchangeToken.
	lastExchange domain.TokenRequest
	// revokeCallCount tracks how many times RevokeToken was called.
	revokeCallCount int
}

func (m *mockHydraPublic) ExchangeToken(_ context.Context, req domain.TokenRequest) (*domain.TokenSet, error) {
	m.lastExchange = req
	return m.exchangeResp, m.exchangeErr
}

func (m *mockHydraPublic) RevokeToken(_ context.Context, _ string) error {
	m.revokeCallCount++
	return m.revokeErr
}

func TestExchangeToken_Success(t *testing.T) {
	mock := &mockHydraPublic{
		exchangeResp: &domain.TokenSet{
			AccessToken:  "at_123",
			RefreshToken: "rt_456",
			TokenType:    "bearer",
			ExpiresIn:    3600,
		},
	}
	handler := NewCookieTokenHandler(mock, "test-client-id", false, nopLogger(t))

	body := `{"code":"auth-code","code_verifier":"verifier",` +
		`"client_id":"spa","redirect_uri":"http://localhost/callback"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/v1/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ExchangeToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verifies no tokens in response body.
	var resp exchangeTokenResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.ExpiresIn != 3600 {
		t.Errorf("expires_in = %d, want 3600", resp.ExpiresIn)
	}

	bodyStr := rr.Body.String()
	if strings.Contains(bodyStr, "at_123") || strings.Contains(bodyStr, "rt_456") {
		t.Error("response body should not contain tokens")
	}

	// Verifies cookies are set.
	cookies := rr.Result().Cookies()
	accessCookie := findCookie(cookies, accessTokenCookie)
	refreshCookie := findCookie(cookies, refreshTokenCookie)

	if accessCookie == nil {
		t.Fatal("expected access token cookie")
	}
	if accessCookie.Value != "at_123" {
		t.Errorf("access cookie value = %q, want %q", accessCookie.Value, "at_123")
	}
	if !accessCookie.HttpOnly {
		t.Error("access cookie should be HttpOnly")
	}
	if accessCookie.MaxAge != 3600 {
		t.Errorf("access cookie MaxAge = %d, want 3600", accessCookie.MaxAge)
	}

	if refreshCookie == nil {
		t.Fatal("expected refresh token cookie")
	}
	if refreshCookie.Value != "rt_456" {
		t.Errorf("refresh cookie value = %q, want %q", refreshCookie.Value, "rt_456")
	}
	if !refreshCookie.HttpOnly {
		t.Error("refresh cookie should be HttpOnly")
	}

	// Verifies correct params were sent to Hydra.
	if mock.lastExchange.GrantType != "authorization_code" {
		t.Errorf("grant_type = %q, want authorization_code", mock.lastExchange.GrantType)
	}
	if mock.lastExchange.Code != "auth-code" {
		t.Errorf("code = %q, want auth-code", mock.lastExchange.Code)
	}
}

func TestExchangeToken_MissingFields(t *testing.T) {
	handler := NewCookieTokenHandler(&mockHydraPublic{}, "test-client-id", false, nopLogger(t))

	tests := []struct {
		name string
		body string
	}{
		{"missing code", `{"code_verifier":"v","client_id":"c","redirect_uri":"r"}`},
		{"missing verifier", `{"code":"c","client_id":"c","redirect_uri":"r"}`},
		{"missing client_id", `{"code":"c","code_verifier":"v","redirect_uri":"r"}`},
		{"missing redirect_uri", `{"code":"c","code_verifier":"v","client_id":"c"}`},
		{"empty body", `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/v1/token", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			handler.ExchangeToken(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestExchangeToken_HydraError(t *testing.T) {
	mock := &mockHydraPublic{exchangeErr: errors.New("invalid_grant")}
	handler := NewCookieTokenHandler(mock, "test-client-id", false, nopLogger(t))

	body := `{"code":"bad-code","code_verifier":"v","client_id":"c","redirect_uri":"r"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/v1/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ExchangeToken(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
}

func TestRefreshToken_Success(t *testing.T) {
	mock := &mockHydraPublic{
		exchangeResp: &domain.TokenSet{
			AccessToken:  "at_new",
			RefreshToken: "rt_new",
			TokenType:    "bearer",
			ExpiresIn:    1800,
		},
	}
	handler := NewCookieTokenHandler(mock, "test-client-id", false, nopLogger(t))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/v1/token/refresh", nil)
	req.AddCookie(&http.Cookie{Name: refreshTokenCookie, Value: "rt_old"})
	rr := httptest.NewRecorder()

	handler.RefreshToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	cookies := rr.Result().Cookies()
	accessCookie := findCookie(cookies, accessTokenCookie)
	if accessCookie == nil || accessCookie.Value != "at_new" {
		t.Errorf("expected new access token cookie, got %v", accessCookie)
	}

	if mock.lastExchange.GrantType != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", mock.lastExchange.GrantType)
	}
	if mock.lastExchange.ClientID != "test-client-id" {
		t.Errorf("client_id = %q, want test-client-id", mock.lastExchange.ClientID)
	}
}

func TestRefreshToken_MissingCookie(t *testing.T) {
	handler := NewCookieTokenHandler(&mockHydraPublic{}, "test-client-id", false, nopLogger(t))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/v1/token/refresh", nil)
	rr := httptest.NewRecorder()

	handler.RefreshToken(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestRefreshToken_HydraRejects(t *testing.T) {
	mock := &mockHydraPublic{exchangeErr: errors.New("invalid_grant")}
	handler := NewCookieTokenHandler(mock, "test-client-id", false, nopLogger(t))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/v1/token/refresh", nil)
	req.AddCookie(&http.Cookie{Name: refreshTokenCookie, Value: "expired_rt"})
	rr := httptest.NewRecorder()

	handler.RefreshToken(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}

	// Verifies cookies are cleared on refresh failure.
	cookies := rr.Result().Cookies()
	accessCookie := findCookie(cookies, accessTokenCookie)
	if accessCookie == nil || accessCookie.MaxAge != -1 {
		t.Error("expected access cookie to be cleared on refresh failure")
	}
}

func TestLogout_Success(t *testing.T) {
	mock := &mockHydraPublic{}
	handler := NewCookieTokenHandler(mock, "test-client-id", false, nopLogger(t))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/v1/logout", nil)
	req.AddCookie(&http.Cookie{Name: refreshTokenCookie, Value: "rt_to_revoke"})
	rr := httptest.NewRecorder()

	handler.Logout(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}

	if mock.revokeCallCount != 1 {
		t.Errorf("revokeCallCount = %d, want 1", mock.revokeCallCount)
	}

	// Verifies cookies are cleared.
	cookies := rr.Result().Cookies()
	accessCookie := findCookie(cookies, accessTokenCookie)
	refreshCookie := findCookie(cookies, refreshTokenCookie)

	if accessCookie == nil || accessCookie.MaxAge != -1 {
		t.Error("expected access cookie to be cleared")
	}
	if refreshCookie == nil || refreshCookie.MaxAge != -1 {
		t.Error("expected refresh cookie to be cleared")
	}
}

func TestLogout_NoRefreshCookie(t *testing.T) {
	mock := &mockHydraPublic{}
	handler := NewCookieTokenHandler(mock, "test-client-id", false, nopLogger(t))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/v1/logout", nil)
	rr := httptest.NewRecorder()

	handler.Logout(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}

	if mock.revokeCallCount != 0 {
		t.Errorf("revokeCallCount = %d, want 0 (no cookie to revoke)", mock.revokeCallCount)
	}
}

func TestExchangeToken_SecureCookies(t *testing.T) {
	mock := &mockHydraPublic{
		exchangeResp: &domain.TokenSet{
			AccessToken:  "at",
			RefreshToken: "rt",
			TokenType:    "bearer",
			ExpiresIn:    3600,
		},
	}
	handler := NewCookieTokenHandler(mock, "test-client-id", true, nopLogger(t))

	body := `{"code":"c","code_verifier":"v","client_id":"id","redirect_uri":"r"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/v1/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ExchangeToken(rr, req)

	cookies := rr.Result().Cookies()
	accessCookie := findCookie(cookies, accessTokenCookie)
	if accessCookie == nil || !accessCookie.Secure {
		t.Error("expected Secure flag on access cookie when secureCookies is true")
	}
}

// --- helpers ---

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func nopLogger(t *testing.T) zerolog.Logger {
	t.Helper()
	return zerolog.Nop()
}
