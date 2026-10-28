//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/api"
	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
)

// errFakeExchange is the sentinel error returned by fakeOIDCProvider.ExchangeCode for the generic-error case.
var errFakeExchange = errors.New("exchange failed")

// fakeOIDCProvider implements service.OIDCProvider and its login button for tests — no network, fully in-memory.
type fakeOIDCProvider struct {
	name             string
	displayName      string
	authURL          string
	exchangeResult   domain.OIDCUserInfo
	exchangeErr      error
	buttonTemplate   *template.Template
	stylesheet       []byte
	gotState         string
	gotNonce         string
	gotCodeChallenge string
	authURLCalls     int
	exchangeCalls    int
}

func (f *fakeOIDCProvider) Name() string        { return f.name }
func (f *fakeOIDCProvider) DisplayName() string { return f.displayName }

func (f *fakeOIDCProvider) AuthorizationURL(state, nonce, codeChallenge string) (string, error) {
	f.authURLCalls++
	f.gotState = state
	f.gotNonce = nonce
	f.gotCodeChallenge = codeChallenge
	return f.authURL, nil
}

func (f *fakeOIDCProvider) ExchangeCode(
	_ context.Context, _, _, _ string,
) (domain.OIDCUserInfo, error) {
	f.exchangeCalls++
	if f.exchangeErr != nil {
		return domain.OIDCUserInfo{}, f.exchangeErr
	}
	return f.exchangeResult, nil
}

func (f *fakeOIDCProvider) ButtonTemplate() *template.Template { return f.buttonTemplate }
func (f *fakeOIDCProvider) Stylesheet() []byte                 { return f.stylesheet }

// mockHydraAdmin stands in for the authorization server the login flow answers, capturing AcceptLogin calls so each
// case can assert on them.
type mockHydraAdmin struct {
	loginChallengeResult *domain.LoginRequest
	loginChallengeErr    error
	acceptLoginChallenge string
	acceptLoginReq       domain.LoginAcceptance
	acceptLoginCalls     int
	acceptLoginRedirect  string
}

func (m *mockHydraAdmin) GetLoginRequest(_ context.Context, _ string) (*domain.LoginRequest, error) {
	if m.loginChallengeErr != nil {
		return nil, m.loginChallengeErr
	}
	return m.loginChallengeResult, nil
}

func (m *mockHydraAdmin) AcceptLogin(_ context.Context, challenge string, req domain.LoginAcceptance) (string, error) {
	m.acceptLoginCalls++
	m.acceptLoginChallenge = challenge
	m.acceptLoginReq = req
	if m.acceptLoginRedirect == "" {
		return "https://hydra.example.com/post-login", nil
	}
	return m.acceptLoginRedirect, nil
}

func (m *mockHydraAdmin) GetConsentRequest(_ context.Context, _ string) (*domain.ConsentRequest, error) {
	return nil, nil
}

func (m *mockHydraAdmin) AcceptConsent(_ context.Context, _ string, _ domain.ConsentGrant) (string, error) {
	return "", nil
}

func (m *mockHydraAdmin) RejectConsent(_ context.Context, _ string, _ domain.Rejection) (string, error) {
	return "", nil
}

// fakeSignupHook implements api.SignupInterstitialHandler for tests. RenderInterstitial captures the SignupContext
// and synchronously calls CompletionAPI.CompleteSignup with a closure that creates the user + the external_identity
// link. Production handlers typically render a form and complete asynchronously via a separate POST handler; the
// synchronous variant is enough to exercise the hook wiring end-to-end.
type fakeSignupHook struct {
	capturedCtx   api.SignupContext
	createdUserID uuid.UUID
	invocations   int
	completeErr   error
}

func (h *fakeSignupHook) RenderInterstitial(
	w http.ResponseWriter, r *http.Request, ctx api.SignupContext, c api.CompletionAPI,
) {
	h.invocations++
	h.capturedCtx = ctx

	err := c.CompleteSignup(r.Context(), w, r, func(cctx context.Context) (uuid.UUID, error) {
		userID := uuid.New()
		h.createdUserID = userID
		now := time.Now().UTC()
		user := &domain.User{
			ID:              userID,
			Email:           ctx.Email,
			PasswordHash:    nil,
			FirstName:       ctx.FirstName,
			LastName:        ctx.LastName,
			CreatedAt:       now,
			UpdatedAt:       now,
			IsActive:        true,
			EmailVerifiedAt: &now,
		}
		if err := repos().users.Create(cctx, user); err != nil {
			return uuid.Nil, err
		}
		link := &domain.ExternalIdentity{
			ID:              uuid.New(),
			UserID:          userID,
			Provider:        ctx.Provider,
			ProviderSubject: ctx.Subject,
			Email:           ctx.Email,
			CreatedAt:       now,
		}
		if err := repos().identities.Create(cctx, link); err != nil {
			return uuid.Nil, err
		}
		return userID, nil
	})
	if err != nil {
		h.completeErr = err
		http.Error(w, "completion failed: "+err.Error(), http.StatusInternalServerError)
	}
}

// oidcFixture wires every dependency for an OIDC handler test against the shared testcontainer pool.
type oidcFixture struct {
	provider *fakeOIDCProvider
	hydra    *mockHydraAdmin
	router   chi.Router
}

func newOIDCFixture(t *testing.T) *oidcFixture {
	return newOIDCFixtureWithHook(t, nil)
}

func newOIDCFixtureWithHook(t *testing.T, hook api.SignupInterstitialHandler) *oidcFixture {
	t.Helper()
	truncateAll(t)

	prov := &fakeOIDCProvider{name: "fake", displayName: "Fake", authURL: "https://fake.example.com/authorize?x=1"}
	hydra := &mockHydraAdmin{}

	r := repos()
	extIdentitySvc := service.NewExternalIdentityService(r.identityTx)
	loginSvc := service.NewOIDCLoginService(extIdentitySvc, r.oidcStates)

	loginFlow := service.NewLoginFlowService(hydra, r.roles)
	parent := api.NewHandler(nil, nil, nil, loginFlow, "", "", testLogger)
	h := api.NewOIDCLoginHandler(
		[]service.OIDCProvider{prov}, loginSvc, loginFlow, hook, parent, testLogger,
	)

	router := chi.NewRouter()
	router.Get("/login/oidc/{provider}/start", h.Start)
	router.Get("/login/oidc/{provider}/callback", h.Callback)

	return &oidcFixture{provider: prov, hydra: hydra, router: router}
}

// seedOIDCState inserts an in-flight flow row directly via the store's repo, standing in for the /start request
// the browser made earlier. Zero CreatedAt/ExpiresAt default to "just started, expires in a minute".
func seedOIDCState(t *testing.T, flow domain.OIDCFlowState) {
	t.Helper()
	if flow.CreatedAt.IsZero() {
		flow.CreatedAt = time.Now().UTC()
	}
	if flow.ExpiresAt.IsZero() {
		flow.ExpiresAt = flow.CreatedAt.Add(time.Minute)
	}
	if err := repos().oidcStates.Create(context.Background(), &flow); err != nil {
		t.Fatalf("seeding oidc flow state: %v", err)
	}
}

func fetchOIDCState(t *testing.T, state string) domain.OIDCFlowState {
	t.Helper()
	var flow domain.OIDCFlowState
	err := testDB.QueryRow(context.Background(),
		`SELECT state, provider, login_challenge, nonce, code_verifier, created_at, expires_at
		 FROM oidc_state WHERE state = $1`, state,
	).Scan(&flow.State, &flow.Provider, &flow.LoginChallenge, &flow.Nonce, &flow.CodeVerifier,
		&flow.CreatedAt, &flow.ExpiresAt)
	if err != nil {
		t.Fatalf("fetching oidc flow state %q: %v", state, err)
	}
	return flow
}

func countOIDCStates(t *testing.T, state string) int {
	t.Helper()
	var n int
	err := testDB.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM oidc_state WHERE state = $1`, state,
	).Scan(&n)
	if err != nil {
		t.Fatalf("counting oidc flow states: %v", err)
	}
	return n
}

func seedUser(t *testing.T, email string, emailVerifiedAt *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	now := time.Now().UTC()
	err := testDB.Exec(context.Background(),
		`INSERT INTO users (id, email, password_hash, first_name, last_name, created_at, updated_at, is_active,
			email_verified_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, true, $8)`,
		id, email, "irrelevant", "First", "Last", now, now, emailVerifiedAt,
	)
	if err != nil {
		t.Fatalf("seeding user: %v", err)
	}
	return id
}

func seedExternalIdentity(t *testing.T, userID uuid.UUID, provider, subject, email string) {
	t.Helper()
	err := testDB.Exec(context.Background(),
		`INSERT INTO external_identities (id, user_id, provider, provider_subject, email, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		uuid.New(), userID, provider, subject, email, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("seeding external identity: %v", err)
	}
}

func countUsers(t *testing.T) int {
	t.Helper()
	var n int
	if err := testDB.QueryRow(context.Background(), `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("counting users: %v", err)
	}
	return n
}

func countExternalIdentities(t *testing.T, provider, subject string) int {
	t.Helper()
	var n int
	err := testDB.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM external_identities WHERE provider = $1 AND provider_subject = $2`,
		provider, subject,
	).Scan(&n)
	if err != nil {
		t.Fatalf("counting external identities: %v", err)
	}
	return n
}

func fetchUserEmailVerifiedAt(t *testing.T, userID uuid.UUID) *time.Time {
	t.Helper()
	var ts *time.Time
	err := testDB.QueryRow(context.Background(),
		`SELECT email_verified_at FROM users WHERE id = $1`, userID,
	).Scan(&ts)
	if err != nil {
		t.Fatalf("fetching email_verified_at: %v", err)
	}
	return ts
}

func fetchUserName(t *testing.T, userID uuid.UUID) (first, last string) {
	t.Helper()
	err := testDB.QueryRow(context.Background(),
		`SELECT first_name, last_name FROM users WHERE id = $1`, userID,
	).Scan(&first, &last)
	if err != nil {
		t.Fatalf("fetching user name: %v", err)
	}
	return first, last
}

// runCallback seeds the given flow row (as if /start had run) and issues the callback GET with the given query
// parameters. queryState is intentionally separate from flow.State so tests can exercise the mismatch case.
func runCallback(
	t *testing.T,
	f *oidcFixture,
	flow domain.OIDCFlowState,
	queryState, queryCode string,
	extraQuery map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	seedOIDCState(t, flow)
	query := url.Values{"state": {queryState}}
	if queryCode != "" {
		query.Set("code", queryCode)
	}
	for k, v := range extraQuery {
		query.Set(k, v)
	}
	req := httptest.NewRequest(http.MethodGet, "/login/oidc/fake/callback?"+query.Encode(), nil)
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

// --- Start ---

func TestOIDCLogin_Start_Success(t *testing.T) {
	f := newOIDCFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/login/oidc/fake/start?login_challenge=lc-abc", nil)
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://fake.example.com/authorize") {
		t.Errorf("Location = %q, want fake authorize URL", loc)
	}
	if f.provider.authURLCalls != 1 {
		t.Errorf("AuthorizationURL called %d times, want 1", f.provider.authURLCalls)
	}
	if f.provider.gotState == "" || f.provider.gotNonce == "" || f.provider.gotCodeChallenge == "" {
		t.Errorf("provider got empty state/nonce/challenge: state=%q nonce=%q chal=%q",
			f.provider.gotState, f.provider.gotNonce, f.provider.gotCodeChallenge)
	}

	if cookies := rr.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("expected no cookies set (flow state is server-side), got %+v", cookies)
	}

	flow := fetchOIDCState(t, f.provider.gotState)
	if flow.LoginChallenge != "lc-abc" || flow.Provider != "fake" || flow.Nonce != f.provider.gotNonce {
		t.Errorf("stored flow state mismatch: %+v", flow)
	}
	if flow.CodeVerifier == "" {
		t.Errorf("stored flow state has empty code verifier")
	}
	if !flow.ExpiresAt.After(time.Now()) {
		t.Errorf("stored flow state already expired: %v", flow.ExpiresAt)
	}
}

func TestOIDCLogin_Start_UnknownProvider(t *testing.T) {
	f := newOIDCFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/login/oidc/ghost/start?login_challenge=lc", nil)
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestOIDCLogin_Start_MissingChallenge(t *testing.T) {
	f := newOIDCFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/login/oidc/fake/start", nil)
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// --- Callback ---

func TestOIDCLogin_Callback_ExistingLink(t *testing.T) {
	f := newOIDCFixture(t)
	verifiedAt := time.Now().UTC().Add(-time.Hour)
	userID := seedUser(t, "alice@example.com", &verifiedAt)
	seedExternalIdentity(t, userID, "fake", "sub-1", "alice@example.com")

	f.provider.exchangeResult = domain.OIDCUserInfo{
		Subject: "sub-1", Email: "alice@example.com", EmailVerified: true,
		FirstName: "GoogleFirst", LastName: "GoogleLast",
	}

	rr := runCallback(t, f, domain.OIDCFlowState{
		LoginChallenge: "lc-abc", Nonce: "n-1", CodeVerifier: "cv-1", State: "s-1", Provider: "fake",
	}, "s-1", "auth-code", nil)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rr.Code, rr.Body.String())
	}
	if f.hydra.acceptLoginCalls != 1 || f.hydra.acceptLoginReq.Subject != userID.String() {
		t.Errorf("AcceptLogin: calls=%d subject=%q want 1 + %q",
			f.hydra.acceptLoginCalls, f.hydra.acceptLoginReq.Subject, userID.String())
	}
	if f.hydra.acceptLoginChallenge != "lc-abc" {
		t.Errorf("AcceptLogin challenge = %q, want %q", f.hydra.acceptLoginChallenge, "lc-abc")
	}

	first, last := fetchUserName(t, userID)
	if first != "First" || last != "Last" {
		t.Errorf("name mutated: got (%q, %q), want (First, Last)", first, last)
	}
	if n := countExternalIdentities(t, "fake", "sub-1"); n != 1 {
		t.Errorf("external_identities rows = %d, want 1 (no new row should be created)", n)
	}
}

func TestOIDCLogin_Callback_ExistingLinkMarksEmailVerified(t *testing.T) {
	f := newOIDCFixture(t)
	// User has an external_identities row from an earlier sign-in but local email_verified_at is still NULL — for
	// example a password-registered user whose link row was inserted by some admin path that did not set the column.
	// A successful OIDC sign-in via this link should populate email_verified_at.
	userID := seedUser(t, "alice@example.com", nil)
	seedExternalIdentity(t, userID, "fake", "sub-1", "alice@example.com")

	f.provider.exchangeResult = domain.OIDCUserInfo{
		Subject: "sub-1", Email: "alice@example.com", EmailVerified: true,
	}

	rr := runCallback(t, f, domain.OIDCFlowState{
		LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv", State: "s", Provider: "fake",
	}, "s", "auth-code", nil)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rr.Code, rr.Body.String())
	}
	if v := fetchUserEmailVerifiedAt(t, userID); v == nil {
		t.Errorf("email_verified_at = NULL, want populated on existing-link sign-in")
	}
	if n := countExternalIdentities(t, "fake", "sub-1"); n != 1 {
		t.Errorf("external_identities rows = %d, want 1 (no new row should be created)", n)
	}
}

func TestOIDCLogin_Callback_EmailMatchLinks(t *testing.T) {
	f := newOIDCFixture(t)
	userID := seedUser(t, "alice@example.com", nil)

	f.provider.exchangeResult = domain.OIDCUserInfo{
		Subject: "sub-2", Email: "alice@example.com", EmailVerified: true,
		FirstName: "GoogleFirst", LastName: "GoogleLast",
	}

	rr := runCallback(t, f, domain.OIDCFlowState{
		LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv", State: "s", Provider: "fake",
	}, "s", "auth-code", nil)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rr.Code, rr.Body.String())
	}
	if f.hydra.acceptLoginCalls != 1 || f.hydra.acceptLoginReq.Subject != userID.String() {
		t.Errorf("AcceptLogin: calls=%d subject=%q want 1 + %q",
			f.hydra.acceptLoginCalls, f.hydra.acceptLoginReq.Subject, userID.String())
	}
	if n := countExternalIdentities(t, "fake", "sub-2"); n != 1 {
		t.Errorf("external_identities rows = %d, want 1 (one new row should be created)", n)
	}
	if v := fetchUserEmailVerifiedAt(t, userID); v == nil {
		t.Errorf("email_verified_at = NULL, want populated")
	}
	first, last := fetchUserName(t, userID)
	if first != "First" || last != "Last" {
		t.Errorf("name mutated by IdP profile: got (%q, %q), want (First, Last)", first, last)
	}
}

func TestOIDCLogin_Callback_NeedsSignup_NoHook(t *testing.T) {
	f := newOIDCFixture(t)
	f.provider.exchangeResult = domain.OIDCUserInfo{
		Subject: "sub-new", Email: "newuser@example.com", EmailVerified: true,
	}

	rr := runCallback(t, f, domain.OIDCFlowState{
		LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv", State: "s", Provider: "fake",
	}, "s", "code", nil)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if f.hydra.acceptLoginCalls != 0 {
		t.Errorf("AcceptLogin called %d times, want 0", f.hydra.acceptLoginCalls)
	}
	if n := countExternalIdentities(t, "fake", "sub-new"); n != 0 {
		t.Errorf("external_identities rows = %d, want 0", n)
	}
	if n := countUsers(t); n != 0 {
		t.Errorf("users rows = %d, want 0", n)
	}
}

func TestOIDCLogin_Callback_NeedsSignup_HookCompletes(t *testing.T) {
	hook := &fakeSignupHook{}
	f := newOIDCFixtureWithHook(t, hook)
	f.provider.exchangeResult = domain.OIDCUserInfo{
		Subject: "sub-new", Email: "newuser@example.com", EmailVerified: true,
		FirstName: "New", LastName: "User",
	}

	rr := runCallback(t, f, domain.OIDCFlowState{
		LoginChallenge: "lc-signup", Nonce: "n", CodeVerifier: "cv", State: "s", Provider: "fake",
	}, "s", "code", nil)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rr.Code, rr.Body.String())
	}
	if hook.invocations != 1 {
		t.Fatalf("hook invocations = %d, want 1", hook.invocations)
	}
	gotCtx := hook.capturedCtx
	if gotCtx.Challenge != "lc-signup" || gotCtx.Provider != "fake" || gotCtx.Subject != "sub-new" ||
		gotCtx.Email != "newuser@example.com" || gotCtx.FirstName != "New" || gotCtx.LastName != "User" {
		t.Errorf("SignupContext = %+v, want all fields populated from OIDCUserInfo + state cookie", gotCtx)
	}
	if f.hydra.acceptLoginCalls != 1 || f.hydra.acceptLoginChallenge != "lc-signup" {
		t.Errorf("AcceptLogin calls=%d challenge=%q, want 1 + %q",
			f.hydra.acceptLoginCalls, f.hydra.acceptLoginChallenge, "lc-signup")
	}
	if f.hydra.acceptLoginReq.Subject != hook.createdUserID.String() {
		t.Errorf("AcceptLogin subject = %q, want %q", f.hydra.acceptLoginReq.Subject, hook.createdUserID.String())
	}
	if n := countUsers(t); n != 1 {
		t.Errorf("users rows = %d, want 1", n)
	}
	if n := countExternalIdentities(t, "fake", "sub-new"); n != 1 {
		t.Errorf("external_identities rows = %d, want 1", n)
	}
}

func TestOIDCLogin_Callback_EmailUnverified(t *testing.T) {
	f := newOIDCFixture(t)
	f.provider.exchangeResult = domain.OIDCUserInfo{
		Subject: "sub-1", Email: "alice@example.com", EmailVerified: false,
	}

	rr := runCallback(t, f, domain.OIDCFlowState{
		LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv", State: "s", Provider: "fake",
	}, "s", "code", nil)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if f.hydra.acceptLoginCalls != 0 {
		t.Errorf("AcceptLogin called %d times, want 0", f.hydra.acceptLoginCalls)
	}
}

func TestOIDCLogin_Callback_ExchangeError(t *testing.T) {
	f := newOIDCFixture(t)
	f.provider.exchangeErr = errFakeExchange

	rr := runCallback(t, f, domain.OIDCFlowState{
		LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv", State: "s", Provider: "fake",
	}, "s", "code", nil)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if f.hydra.acceptLoginCalls != 0 {
		t.Errorf("AcceptLogin called %d times, want 0", f.hydra.acceptLoginCalls)
	}
}

func TestOIDCLogin_Callback_StateMismatch(t *testing.T) {
	f := newOIDCFixture(t)
	rr := runCallback(t, f, domain.OIDCFlowState{
		LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv", State: "real-state", Provider: "fake",
	}, "tampered-state", "code", nil)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if f.provider.exchangeCalls != 0 {
		t.Errorf("ExchangeCode called %d times, want 0", f.provider.exchangeCalls)
	}
}

func TestOIDCLogin_Callback_MissingCode(t *testing.T) {
	f := newOIDCFixture(t)
	rr := runCallback(t, f, domain.OIDCFlowState{
		LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv", State: "s", Provider: "fake",
	}, "s", "", nil)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestOIDCLogin_Callback_IdPError(t *testing.T) {
	f := newOIDCFixture(t)
	rr := runCallback(t, f, domain.OIDCFlowState{
		LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv", State: "s", Provider: "fake",
	}, "s", "code", map[string]string{"error": "access_denied"})

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if f.provider.exchangeCalls != 0 {
		t.Errorf("ExchangeCode called %d times, want 0", f.provider.exchangeCalls)
	}
}

func TestOIDCLogin_Callback_UnknownState(t *testing.T) {
	f := newOIDCFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/login/oidc/fake/callback?code=ac&state=never-issued", nil)
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if f.provider.exchangeCalls != 0 {
		t.Errorf("ExchangeCode called %d times, want 0", f.provider.exchangeCalls)
	}
}

func TestOIDCLogin_Callback_ExpiredState(t *testing.T) {
	f := newOIDCFixture(t)
	created := time.Now().UTC().Add(-10 * time.Minute)
	seedOIDCState(t, domain.OIDCFlowState{
		State: "s-expired", Provider: "fake", LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv",
		CreatedAt: created, ExpiresAt: created.Add(5 * time.Minute),
	})

	req := httptest.NewRequest(http.MethodGet, "/login/oidc/fake/callback?code=ac&state=s-expired", nil)
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if f.provider.exchangeCalls != 0 {
		t.Errorf("ExchangeCode called %d times, want 0", f.provider.exchangeCalls)
	}
	if n := countOIDCStates(t, "s-expired"); n != 0 {
		t.Errorf("oidc_state rows = %d, want 0 (expired row should be consumed even when rejected)", n)
	}
}

func TestOIDCLogin_Callback_ConcurrentSameState(t *testing.T) {
	f := newOIDCFixture(t)
	verifiedAt := time.Now().UTC().Add(-time.Hour)
	userID := seedUser(t, "alice@example.com", &verifiedAt)
	seedExternalIdentity(t, userID, "fake", "sub-1", "alice@example.com")
	f.provider.exchangeResult = domain.OIDCUserInfo{
		Subject: "sub-1", Email: "alice@example.com", EmailVerified: true,
	}

	seedOIDCState(t, domain.OIDCFlowState{
		State: "s-race", Provider: "fake", LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv",
	})

	// Two simultaneous callbacks carrying the same state value: the atomic consume must let exactly one through.
	const racers = 2
	codes := make(chan int, racers)
	var wg sync.WaitGroup
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/login/oidc/fake/callback?code=ac&state=s-race", nil)
			rr := httptest.NewRecorder()
			f.router.ServeHTTP(rr, req)
			codes <- rr.Code
		}()
	}
	wg.Wait()
	close(codes)

	var succeeded, rejected int
	for code := range codes {
		switch code {
		case http.StatusFound:
			succeeded++
		case http.StatusBadRequest:
			rejected++
		default:
			t.Errorf("unexpected status %d, want 302 or 400", code)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Errorf("got %d successes + %d rejections, want exactly 1 + 1", succeeded, rejected)
	}
	if f.provider.exchangeCalls != 1 {
		t.Errorf("ExchangeCode called %d times, want 1", f.provider.exchangeCalls)
	}
	if f.hydra.acceptLoginCalls != 1 {
		t.Errorf("AcceptLogin called %d times, want 1", f.hydra.acceptLoginCalls)
	}
}

// TestOIDCLogin_PurgeExpiredFlows exercises the purge DELETE at service level — the tick-driven runner is the only
// production path into PurgeExpiredFlows, so no handler-level test ever runs this statement.
func TestOIDCLogin_PurgeExpiredFlows(t *testing.T) {
	truncateAll(t)
	r := repos()
	svc := service.NewOIDCLoginService(service.NewExternalIdentityService(r.identityTx), r.oidcStates)

	created := time.Now().UTC().Add(-10 * time.Minute)
	for _, state := range []string{"s-expired-1", "s-expired-2"} {
		seedOIDCState(t, domain.OIDCFlowState{
			State: state, Provider: "fake", LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv",
			CreatedAt: created, ExpiresAt: created.Add(5 * time.Minute),
		})
	}
	seedOIDCState(t, domain.OIDCFlowState{
		State: "s-live", Provider: "fake", LoginChallenge: "lc", Nonce: "n", CodeVerifier: "cv",
	})

	deleted, err := svc.PurgeExpiredFlows(context.Background())
	if err != nil {
		t.Fatalf("PurgeExpiredFlows: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	for _, state := range []string{"s-expired-1", "s-expired-2"} {
		if n := countOIDCStates(t, state); n != 0 {
			t.Errorf("oidc_state rows for %q = %d, want 0", state, n)
		}
	}
	if n := countOIDCStates(t, "s-live"); n != 1 {
		t.Errorf("oidc_state rows for s-live = %d, want 1 (unexpired row must survive the purge)", n)
	}
}
