package service

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// fakeAuthServer answers like an authorization server, returning the requests it holds and recording what the
// service accepts or rejects.
type fakeAuthServer struct {
	login   *domain.LoginRequest
	consent *domain.ConsentRequest

	acceptedLogin   *domain.LoginAcceptance
	acceptedConsent *domain.ConsentGrant
	rejectedConsent *domain.Rejection
}

func (f *fakeAuthServer) GetLoginRequest(_ context.Context, _ string) (*domain.LoginRequest, error) {
	return f.login, nil
}

func (f *fakeAuthServer) AcceptLogin(_ context.Context, _ string, req domain.LoginAcceptance) (string, error) {
	f.acceptedLogin = &req
	return "https://auth.example/after-login", nil
}

func (f *fakeAuthServer) GetConsentRequest(_ context.Context, _ string) (*domain.ConsentRequest, error) {
	return f.consent, nil
}

func (f *fakeAuthServer) AcceptConsent(_ context.Context, _ string, grant domain.ConsentGrant) (string, error) {
	f.acceptedConsent = &grant
	return "https://auth.example/after-consent", nil
}

func (f *fakeAuthServer) RejectConsent(_ context.Context, _ string, req domain.Rejection) (string, error) {
	f.rejectedConsent = &req
	return "https://auth.example/denied", nil
}

// fixedRoles reads the same roles for every user.
type fixedRoles []authz.Role

func (r fixedRoles) GetRoles(_ context.Context, _ uuid.UUID) ([]authz.Role, error) {
	return r, nil
}

// TestLoginFlowService_StartLogin checks that a login the authorization server remembers is accepted right away for
// its subject, without being remembered again, while any other login is handed back for the app to identify the user.
func TestLoginFlowService_StartLogin(t *testing.T) {
	ctx := context.Background()

	server := &fakeAuthServer{login: &domain.LoginRequest{Skip: true, Subject: "user-1"}}
	req, redirectTo, err := NewLoginFlowService(server, fixedRoles{}).StartLogin(ctx, "lc")
	if err != nil || req != nil || redirectTo != "https://auth.example/after-login" {
		t.Fatalf("remembered login = (%v, %q, %v), want it accepted", req, redirectTo, err)
	}
	if server.acceptedLogin.Subject != "user-1" || server.acceptedLogin.Remember {
		t.Errorf("accepted %+v, want subject user-1, not remembered again", *server.acceptedLogin)
	}

	server = &fakeAuthServer{login: &domain.LoginRequest{ClientName: "App"}}
	req, redirectTo, err = NewLoginFlowService(server, fixedRoles{}).StartLogin(ctx, "lc")
	if err != nil || req == nil || req.ClientName != "App" || redirectTo != "" || server.acceptedLogin != nil {
		t.Errorf("fresh login = (%v, %q, %v, accepted %v), want the request back", req, redirectTo, err,
			server.acceptedLogin)
	}
}

// TestLoginFlowService_CompleteLogin checks that a completed login is accepted for the user and remembered.
func TestLoginFlowService_CompleteLogin(t *testing.T) {
	server := &fakeAuthServer{}
	userID := uuid.New()
	redirectTo, err := NewLoginFlowService(server, fixedRoles{}).CompleteLogin(context.Background(), "lc", userID)
	if err != nil || redirectTo != "https://auth.example/after-login" {
		t.Fatalf("CompleteLogin = (%q, %v)", redirectTo, err)
	}
	want := domain.LoginAcceptance{Subject: userID.String(), Remember: true, RememberFor: rememberFor}
	if *server.acceptedLogin != want {
		t.Errorf("accepted %+v, want %+v", *server.acceptedLogin, want)
	}
}

// TestLoginFlowService_Consent checks the consent grants: a skipped consent grants every requested scope without
// remembering it, a granted one only the scopes the user agreed to, remembered; both carry the requested audience and
// the subject's roles in the access and ID token claims. A denied consent is rejected as access_denied.
func TestLoginFlowService_Consent(t *testing.T) {
	ctx := context.Background()
	subject := uuid.NewString()
	roles := fixedRoles{authz.RoleUser, authz.RoleAdmin}
	newRequest := func(skipConsent bool) *domain.ConsentRequest {
		return &domain.ConsentRequest{
			Subject: subject, RequestedScopes: []string{"openid", "offline_access"},
			RequestedAudience: []string{"api"}, SkipConsent: skipConsent,
		}
	}

	server := &fakeAuthServer{consent: newRequest(true)}
	req, redirectTo, err := NewLoginFlowService(server, roles).StartConsent(ctx, "cc")
	if err != nil || req != nil || redirectTo != "https://auth.example/after-consent" {
		t.Fatalf("skipped consent = (%v, %q, %v), want it granted", req, redirectTo, err)
	}
	checkGrant(t, *server.acceptedConsent, []string{"openid", "offline_access"}, false, subject)

	server = &fakeAuthServer{consent: newRequest(false)}
	req, redirectTo, err = NewLoginFlowService(server, roles).StartConsent(ctx, "cc")
	if err != nil || req == nil || redirectTo != "" || server.acceptedConsent != nil {
		t.Fatalf("consent to ask = (%v, %q, %v), want the request back", req, redirectTo, err)
	}
	if _, err := NewLoginFlowService(server, roles).GrantConsent(ctx, "cc", []string{"openid"}); err != nil {
		t.Fatalf("GrantConsent: %v", err)
	}
	checkGrant(t, *server.acceptedConsent, []string{"openid"}, true, subject)

	if _, err := NewLoginFlowService(server, roles).RejectConsent(ctx, "cc"); err != nil {
		t.Fatalf("RejectConsent: %v", err)
	}
	if server.rejectedConsent.Error != "access_denied" {
		t.Errorf("rejected with %q, want access_denied", server.rejectedConsent.Error)
	}
}

// checkGrant reports how grant differs from one of scopes, remembered or not, for subject holding the user and admin
// roles, with the "api" audience.
func checkGrant(t *testing.T, grant domain.ConsentGrant, scopes []string, remembered bool, subject string) {
	t.Helper()
	wantRoles := []string{"user", "admin"}
	if !slices.Equal(grant.Scopes, scopes) || !slices.Equal(grant.Audience, []string{"api"}) {
		t.Errorf("granted scopes %v and audience %v, want %v and [api]", grant.Scopes, grant.Audience, scopes)
	}
	if grant.Remember != remembered || (remembered && grant.RememberFor != rememberFor) {
		t.Errorf("remember = %v for %d, want %v", grant.Remember, grant.RememberFor, remembered)
	}
	if got, _ := grant.AccessTokenClaims["roles"].([]string); !slices.Equal(got, wantRoles) {
		t.Errorf("access token roles = %v, want %v", grant.AccessTokenClaims["roles"], wantRoles)
	}
	if got, _ := grant.IDTokenClaims["roles"].([]string); !slices.Equal(got, wantRoles) ||
		grant.IDTokenClaims["sub"] != subject {
		t.Errorf("ID token claims = %v, want sub %s and roles %v", grant.IDTokenClaims, subject, wantRoles)
	}
}
