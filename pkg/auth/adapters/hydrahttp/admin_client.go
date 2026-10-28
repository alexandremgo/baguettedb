// Package hydrahttp adapts the auth app to the Hydra OAuth2 authorization server over its HTTP APIs: the admin API for
// the login and consent requests Hydra delegates to the app and for OAuth2 client registrations, and the public API for
// token exchange, revocation and dynamic client registration. Hydra's JSON shapes stay in this package, converted to
// and from the domain's types.
package hydrahttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

const httpTimeout = 10 * time.Second

// AdminClient calls Hydra's HTTP Admin API: the login and consent requests it delegates to the app, and the OAuth2
// client registrations.
type AdminClient struct {
	baseURL string
	client  *http.Client
}

// NewAdminClient creates a new Hydra Admin API client.
func NewAdminClient(adminURL string) *AdminClient {
	return &AdminClient{
		baseURL: adminURL,
		client: &http.Client{
			Timeout: httpTimeout,
			// Creates spans for outgoing requests and propagates trace context.
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

// GetLoginRequest fetches the login request challenge identifies.
func (c *AdminClient) GetLoginRequest(ctx context.Context, challenge string) (*domain.LoginRequest, error) {
	var resp loginChallengeResponse
	path := "/admin/oauth2/auth/requests/login?" + url.Values{"login_challenge": {challenge}}.Encode()
	if err := c.get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("getting login challenge: %w", err)
	}
	return resp.toDomain(), nil
}

// AcceptLogin accepts the login request challenge identifies for req's subject and returns where to send the browser.
func (c *AdminClient) AcceptLogin(ctx context.Context, challenge string, req domain.LoginAcceptance) (string, error) {
	body := acceptLoginBody{
		Subject:     req.Subject,
		Remember:    req.Remember,
		RememberFor: req.RememberFor,
	}
	var resp redirectResponse
	path := "/admin/oauth2/auth/requests/login/accept?" + url.Values{"login_challenge": {challenge}}.Encode()
	if err := c.put(ctx, path, body, &resp); err != nil {
		return "", fmt.Errorf("accepting login: %w", err)
	}
	return resp.RedirectTo, nil
}

// RejectLogin rejects the login request challenge identifies with req's error and returns where to send the browser.
func (c *AdminClient) RejectLogin(ctx context.Context, challenge string, req domain.Rejection) (string, error) {
	body := rejectBody{
		Error:            req.Error,
		ErrorDescription: req.ErrorDescription,
	}
	var resp redirectResponse
	path := "/admin/oauth2/auth/requests/login/reject?" + url.Values{"login_challenge": {challenge}}.Encode()
	if err := c.put(ctx, path, body, &resp); err != nil {
		return "", fmt.Errorf("rejecting login: %w", err)
	}
	return resp.RedirectTo, nil
}

// GetConsentRequest fetches the consent request challenge identifies.
func (c *AdminClient) GetConsentRequest(ctx context.Context, challenge string) (*domain.ConsentRequest, error) {
	var resp consentChallengeResponse
	path := "/admin/oauth2/auth/requests/consent?" + url.Values{"consent_challenge": {challenge}}.Encode()
	if err := c.get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("getting consent challenge: %w", err)
	}
	return resp.toDomain(), nil
}

// AcceptConsent grants the consent request challenge identifies as grant describes and returns where to send the
// browser.
func (c *AdminClient) AcceptConsent(ctx context.Context, challenge string, grant domain.ConsentGrant) (string, error) {
	body := acceptConsentBody{
		GrantScope:               grant.Scopes,
		GrantAccessTokenAudience: grant.Audience,
		Remember:                 grant.Remember,
		RememberFor:              grant.RememberFor,
		Session: consentSessionBody{
			AccessToken: grant.AccessTokenClaims,
			IDToken:     grant.IDTokenClaims,
		},
	}
	var resp redirectResponse
	path := "/admin/oauth2/auth/requests/consent/accept?" + url.Values{"consent_challenge": {challenge}}.Encode()
	if err := c.put(ctx, path, body, &resp); err != nil {
		return "", fmt.Errorf("accepting consent: %w", err)
	}
	return resp.RedirectTo, nil
}

// RejectConsent rejects the consent request challenge identifies with req's error and returns where to send the
// browser.
func (c *AdminClient) RejectConsent(ctx context.Context, challenge string, req domain.Rejection) (string, error) {
	body := rejectBody{
		Error:            req.Error,
		ErrorDescription: req.ErrorDescription,
	}
	var resp redirectResponse
	path := "/admin/oauth2/auth/requests/consent/reject?" + url.Values{"consent_challenge": {challenge}}.Encode()
	if err := c.put(ctx, path, body, &resp); err != nil {
		return "", fmt.Errorf("rejecting consent: %w", err)
	}
	return resp.RedirectTo, nil
}

// GetOAuth2Client fetches the client registered under clientID, returning domain.ErrOAuth2ClientNotFound when the
// authorization server knows no such client.
func (c *AdminClient) GetOAuth2Client(ctx context.Context, clientID string) (*domain.OAuth2Client, error) {
	var resp oauth2ClientDTO
	if err := c.get(ctx, "/admin/clients/"+url.PathEscape(clientID), &resp); err != nil {
		if apiErr, ok := errors.AsType[*apiError](err); ok && apiErr.status == http.StatusNotFound {
			return nil, domain.ErrOAuth2ClientNotFound
		}
		return nil, fmt.Errorf("getting oauth2 client: %w", err)
	}
	return resp.toDomain(), nil
}

// CreateOAuth2Client registers client and returns the stored registration, which carries the id the authorization
// server assigned when client.ID was empty. Returns domain.ErrOAuth2ClientExists when client.ID is already taken.
func (c *AdminClient) CreateOAuth2Client(
	ctx context.Context, client domain.OAuth2Client,
) (*domain.OAuth2Client, error) {
	var resp oauth2ClientDTO
	if err := c.post(ctx, "/admin/clients", oauth2ClientFromDomain(client), &resp); err != nil {
		if apiErr, ok := errors.AsType[*apiError](err); ok && apiErr.status == http.StatusConflict {
			return nil, domain.ErrOAuth2ClientExists
		}
		return nil, fmt.Errorf("creating oauth2 client: %w", err)
	}
	return resp.toDomain(), nil
}

// UpdateOAuth2Client replaces the registration stored under client.ID with client and returns the stored result.
func (c *AdminClient) UpdateOAuth2Client(
	ctx context.Context, client domain.OAuth2Client,
) (*domain.OAuth2Client, error) {
	var resp oauth2ClientDTO
	path := "/admin/clients/" + url.PathEscape(client.ID)
	if err := c.put(ctx, path, oauth2ClientFromDomain(client), &resp); err != nil {
		if apiErr, ok := errors.AsType[*apiError](err); ok && apiErr.status == http.StatusNotFound {
			return nil, domain.ErrOAuth2ClientNotFound
		}
		return nil, fmt.Errorf("updating oauth2 client: %w", err)
	}
	return resp.toDomain(), nil
}

// get performs a GET request and decodes the JSON response.
func (c *AdminClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	return c.doJSON(req, out)
}

// put performs a PUT request with a JSON body and decodes the JSON response.
func (c *AdminClient) put(ctx context.Context, path string, body any, out any) error {
	return c.sendJSON(ctx, http.MethodPut, path, body, out)
}

// post performs a POST request with a JSON body and decodes the JSON response.
func (c *AdminClient) post(ctx context.Context, path string, body any, out any) error {
	return c.sendJSON(ctx, http.MethodPost, path, body, out)
}

// sendJSON performs a request of the given method with a JSON body and decodes the JSON response.
func (c *AdminClient) sendJSON(ctx context.Context, method, path string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	return c.doJSON(req, out)
}

// apiError is a non-2xx answer from the admin API, kept typed so callers can map specific status codes to domain
// errors.
type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("hydra admin API error (status %d): %s", e.status, e.body)
}

// doJSON executes an HTTP request and decodes the JSON response.
func (c *AdminClient) doJSON(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return &apiError{status: resp.StatusCode, body: string(respBody)}
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	return nil
}

// Defines internal JSON DTOs matching Hydra's snake_case API.

type loginChallengeResponse struct {
	Challenge      string   `json:"challenge"`
	RequestedScope []string `json:"requested_scope"`
	Subject        string   `json:"subject"`
	Skip           bool     `json:"skip"`
	RequestURL     string   `json:"request_url"`
	Client         client   `json:"client"`
}

type consentChallengeResponse struct {
	Challenge         string   `json:"challenge"`
	RequestedScope    []string `json:"requested_scope"`
	RequestedAudience []string `json:"requested_access_token_audience"`
	Subject           string   `json:"subject"`
	Skip              bool     `json:"skip"`
	Client            client   `json:"client"`
}

type client struct {
	ClientID    string `json:"client_id"`
	ClientName  string `json:"client_name"`
	SkipConsent bool   `json:"skip_consent"`
}

// oauth2ClientDTO mirrors the fields of Hydra's client registration that the platform manages. client_id is
// omitted when empty so Hydra assigns one.
type oauth2ClientDTO struct {
	ClientID                string   `json:"client_id,omitempty"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	SkipConsent             bool     `json:"skip_consent"`
}

func oauth2ClientFromDomain(c domain.OAuth2Client) oauth2ClientDTO {
	return oauth2ClientDTO{
		ClientID:                c.ID,
		ClientName:              c.Name,
		RedirectURIs:            c.RedirectURIs,
		GrantTypes:              c.GrantTypes,
		ResponseTypes:           c.ResponseTypes,
		Scope:                   c.Scope,
		TokenEndpointAuthMethod: c.TokenEndpointAuthMethod,
		SkipConsent:             c.SkipConsent,
	}
}

func (d *oauth2ClientDTO) toDomain() *domain.OAuth2Client {
	return &domain.OAuth2Client{
		ID:                      d.ClientID,
		Name:                    d.ClientName,
		RedirectURIs:            d.RedirectURIs,
		GrantTypes:              d.GrantTypes,
		ResponseTypes:           d.ResponseTypes,
		Scope:                   d.Scope,
		TokenEndpointAuthMethod: d.TokenEndpointAuthMethod,
		SkipConsent:             d.SkipConsent,
	}
}

type acceptLoginBody struct {
	Subject     string `json:"subject"`
	Remember    bool   `json:"remember"`
	RememberFor int    `json:"remember_for"`
}

type acceptConsentBody struct {
	GrantScope               []string           `json:"grant_scope"`
	GrantAccessTokenAudience []string           `json:"grant_access_token_audience"`
	Remember                 bool               `json:"remember"`
	RememberFor              int                `json:"remember_for"`
	Session                  consentSessionBody `json:"session"`
}

type consentSessionBody struct {
	AccessToken map[string]any `json:"access_token"`
	IDToken     map[string]any `json:"id_token"`
}

type rejectBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type redirectResponse struct {
	RedirectTo string `json:"redirect_to"`
}

func (r *loginChallengeResponse) toDomain() *domain.LoginRequest {
	return &domain.LoginRequest{
		Challenge:       r.Challenge,
		RequestedScopes: r.RequestedScope,
		Subject:         r.Subject,
		Skip:            r.Skip,
		RequestURL:      r.RequestURL,
		ClientID:        r.Client.ClientID,
		ClientName:      r.Client.ClientName,
	}
}

func (r *consentChallengeResponse) toDomain() *domain.ConsentRequest {
	return &domain.ConsentRequest{
		Challenge:         r.Challenge,
		RequestedScopes:   r.RequestedScope,
		RequestedAudience: r.RequestedAudience,
		Subject:           r.Subject,
		Skip:              r.Skip,
		SkipConsent:       r.Client.SkipConsent,
		ClientID:          r.Client.ClientID,
		ClientName:        r.Client.ClientName,
	}
}
