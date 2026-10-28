//go:build integration

package integrationtest

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/alexandremgo/baguettedb/pkg/auth/api"
	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// setupLoginRouter wires the GET /login route with a custom Hydra fake and a configurable set of OIDC providers.
// The OIDCLoginHandler is mounted so the dynamic stylesheet route at /static/oidc/{provider}.css is reachable too.
func setupLoginRouter(
	t *testing.T, hydra *mockHydraAdmin, providers []service.OIDCProvider,
) chi.Router {
	t.Helper()
	truncateAll(t)

	r := repos()
	authenticationSvc := service.NewAuthenticationService(r.users)
	userSvc := service.NewUserService(r.users, r.userTx, testLogger)
	roleSvc := service.NewRoleService(r.roles, authz.Policy{})

	loginFlow := service.NewLoginFlowService(hydra, r.roles)
	handler := api.NewHandler(authenticationSvc, userSvc, roleSvc, loginFlow, "", "", testLogger)
	if len(providers) > 0 {
		handler.SetOIDCProviders(providers)
	}
	internalHandler := api.NewInternalHandler(roleSvc, userSvc)
	discovery := api.NewDiscoveryHandler("http://localhost:8080", testJWKS.Issuer(), testLogger)
	oidcProxy := api.NewOIDCProxyHandler(nil, "http://localhost:8080", testLogger)
	tv, err := authhttp.NewTokenValidator(
		context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger,
	)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	var oidcLogin *api.OIDCLoginHandler
	if len(providers) > 0 {
		extIdentitySvc := service.NewExternalIdentityService(r.identityTx)
		loginSvc := service.NewOIDCLoginService(extIdentitySvc, r.oidcStates)
		oidcLogin = api.NewOIDCLoginHandler(
			providers, loginSvc, loginFlow, nil, handler, testLogger,
		)
	}

	router := api.NewRouter(handler, internalHandler, tv.RequireToken(), authz.Policy{}, testutil.InternalAPISecret)
	api.RegisterOAuthRoutes(router, handler, discovery, oidcProxy, nil, nil, oidcLogin)
	return router
}

// buttonTpl parses a minimal button template that echoes Href + DisplayName so assertions can check both the route
// wiring and the provider label passing through to the rendered HTML.
func buttonTpl(t *testing.T, name string) *template.Template {
	t.Helper()
	tpl, err := template.New(name).Parse(
		`<a class="fake-btn fake-btn-` + name + `" href="{{.Href}}" aria-label="Sign in with {{.DisplayName}}">` +
			`Sign in with {{.DisplayName}}</a>`,
	)
	if err != nil {
		t.Fatalf("parsing button template %q: %v", name, err)
	}
	return tpl
}

func TestLogin_RendersProviderButtons(t *testing.T) {
	hydra := &mockHydraAdmin{
		loginChallengeResult: &domain.LoginRequest{ClientID: "third-party-app", ClientName: "Third-Party App"},
	}
	// One provider ships a stylesheet, the other does not — exercises both branches of the <link rel="stylesheet">
	// gate in the page <head>.
	withCSS := &fakeOIDCProvider{
		name: "with-css", displayName: "With CSS",
		buttonTemplate: buttonTpl(t, "with-css"),
		stylesheet:     []byte(`.fake-btn-with-css { color: red; }`),
	}
	noCSS := &fakeOIDCProvider{
		name: "no-css", displayName: "No CSS",
		buttonTemplate: buttonTpl(t, "no-css"),
	}
	router := setupLoginRouter(t, hydra, []service.OIDCProvider{withCSS, noCSS})

	req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=lc-abc", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()

	wantHrefs := []string{
		`href="/login/oidc/with-css/start?login_challenge=lc-abc"`,
		`href="/login/oidc/no-css/start?login_challenge=lc-abc"`,
	}
	for _, h := range wantHrefs {
		if !strings.Contains(body, h) {
			t.Errorf("missing button href %q; body excerpt:\n%s", h, snippet(body, "oidc-buttons"))
		}
	}
	wantLabels := []string{"Sign in with With CSS", "Sign in with No CSS"}
	for _, l := range wantLabels {
		if !strings.Contains(body, l) {
			t.Errorf("missing label %q", l)
		}
	}
	if !strings.Contains(body, `href="/static/oidc/with-css.css"`) {
		t.Errorf("missing <link rel=stylesheet> for provider with CSS")
	}
	if strings.Contains(body, `href="/static/oidc/no-css.css"`) {
		t.Errorf("rendered <link rel=stylesheet> for provider without CSS")
	}
	if got, want := strings.Count(body, "fake-btn-"), 2; got != want {
		t.Errorf("rendered %d provider buttons, want %d", got, want)
	}
}

func TestLogin_ServeStylesheet(t *testing.T) {
	hydra := &mockHydraAdmin{
		loginChallengeResult: &domain.LoginRequest{ClientID: "x", ClientName: "X"},
	}
	css := []byte(`.fake-btn-with-css { color: red; }`)
	prov := &fakeOIDCProvider{
		name: "with-css", displayName: "With CSS",
		buttonTemplate: buttonTpl(t, "with-css"),
		stylesheet:     css,
	}
	router := setupLoginRouter(t, hydra, []service.OIDCProvider{prov})

	t.Run("known provider with CSS returns its bytes + text/css", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/oidc/with-css.css", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if got := rr.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
			t.Errorf("Content-Type = %q, want text/css; charset=utf-8", got)
		}
		if rr.Body.String() != string(css) {
			t.Errorf("body mismatch")
		}
	})

	t.Run("unknown provider returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/oidc/ghost.css", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})

	t.Run("provider with empty stylesheet returns 404", func(t *testing.T) {
		hydraB := &mockHydraAdmin{loginChallengeResult: &domain.LoginRequest{ClientID: "x"}}
		noCSSRouter := setupLoginRouter(t, hydraB, []service.OIDCProvider{
			&fakeOIDCProvider{name: "blank", displayName: "Blank", buttonTemplate: buttonTpl(t, "blank")},
		})

		req := httptest.NewRequest(http.MethodGet, "/static/oidc/blank.css", nil)
		rr := httptest.NewRecorder()
		noCSSRouter.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (provider opted out of stylesheet)", rr.Code)
		}
	})
}

func TestLogin_NoProviders_NoButtons(t *testing.T) {
	hydra := &mockHydraAdmin{
		loginChallengeResult: &domain.LoginRequest{ClientID: "third-party-app", ClientName: "Third-Party App"},
	}
	router := setupLoginRouter(t, hydra, nil)

	req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=lc-abc", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()

	if strings.Contains(body, `<div class="oidc-buttons">`) || strings.Contains(body, "fake-btn-") {
		t.Errorf("expected no OIDC button block, found one in body:\n%s", body)
	}
	if !strings.Contains(body, `name="email"`) || !strings.Contains(body, `name="password"`) {
		t.Errorf("expected email + password form to render alone; body=%s", body)
	}
}

// TestLogin_NilButtonTemplate_SkipsProvider covers the "provider returns nil template" branch — the framework logs
// and skips, the rest of the page still renders, and the route for /start remains live for direct nav.
func TestLogin_NilButtonTemplate_SkipsProvider(t *testing.T) {
	hydra := &mockHydraAdmin{
		loginChallengeResult: &domain.LoginRequest{ClientID: "x", ClientName: "X"},
	}
	silent := &fakeOIDCProvider{name: "silent", displayName: "Silent"}
	visible := &fakeOIDCProvider{
		name: "visible", displayName: "Visible", buttonTemplate: buttonTpl(t, "visible"),
	}
	router := setupLoginRouter(t, hydra, []service.OIDCProvider{silent, visible})

	req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=lc-abc", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	if strings.Contains(body, "fake-btn-silent") {
		t.Errorf("silent provider rendered despite nil button template")
	}
	if !strings.Contains(body, "fake-btn-visible") {
		t.Errorf("visible provider did not render alongside the silent one")
	}
}

// TestLogin_ProviderWithoutButton checks that a provider offering no login button gets none on the login page, next to
// the button of a provider offering one, serves no stylesheet, and still starts its sign-in flow from its route.
func TestLogin_ProviderWithoutButton(t *testing.T) {
	hydra := &mockHydraAdmin{
		loginChallengeResult: &domain.LoginRequest{ClientID: "x", ClientName: "X"},
	}
	// Embedding the interface promotes only the port's methods, hiding the fake's button ones.
	routeOnly := struct{ service.OIDCProvider }{&fakeOIDCProvider{
		name: "route-only", displayName: "Route Only", authURL: "https://route-only.example/authorize",
		buttonTemplate: buttonTpl(t, "route-only"), stylesheet: []byte(`.fake-btn-route-only { color: red; }`),
	}}
	visible := &fakeOIDCProvider{name: "visible", displayName: "Visible", buttonTemplate: buttonTpl(t, "visible")}
	router := setupLoginRouter(t, hydra, []service.OIDCProvider{routeOnly, visible})
	serve := func(target string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		return rr
	}

	rr := serve("/login?login_challenge=lc-abc")
	if rr.Code != http.StatusOK {
		t.Fatalf("login page status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "fake-btn-route-only") || strings.Contains(body, "/static/oidc/route-only.css") {
		t.Errorf("provider without a login button rendered one; body excerpt:\n%s", snippet(body, "route-only"))
	}
	if !strings.Contains(body, "fake-btn-visible") {
		t.Errorf("provider with a login button did not render alongside the other one")
	}

	if rr := serve("/static/oidc/route-only.css"); rr.Code != http.StatusNotFound {
		t.Errorf("stylesheet status = %d, want 404", rr.Code)
	}
	rr = serve("/login/oidc/route-only/start?login_challenge=lc-abc")
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "https://route-only.example/authorize" {
		t.Errorf("start = %d to %q, want 302 to the provider", rr.Code, rr.Header().Get("Location"))
	}
}

// snippet returns a short window of body around the first occurrence of needle, for nicer failure output. Returns
// the full body when needle is absent.
func snippet(body, needle string) string {
	idx := strings.Index(body, needle)
	if idx < 0 {
		return body
	}
	start := max(idx-80, 0)
	end := min(idx+320, len(body))
	return body[start:end]
}
