// Package client is a pure REST client for the Infisical API. It carries no
// Pulumi dependency, so the request/response contract can be exercised directly
// in unit tests (httptest) and integration tests (a real self-hosted Infisical),
// independent of the Pulumi resource lifecycle that drives it in production.
package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Client talks to a single Infisical instance. Construct it with New, then call
// Authenticate (Universal Auth) to obtain a bearer token, which is stored and
// reused by subsequent calls. A Client is not safe for concurrent use; create
// one per resource operation.
type Client struct {
	baseURL string
	http    *http.Client
	token   string
}

// New returns a Client for the Infisical instance at baseURL (e.g.
// "https://app.infisical.com"). A trailing slash is trimmed.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    http.DefaultClient,
	}
}

// SetToken sets the bearer token directly, bypassing Authenticate. Intended for
// callers that already hold a token — e.g. an integration-test bootstrap that
// logs in as an admin user to seed the instance.
func (c *Client) SetToken(token string) { c.token = token }

// Token returns the current bearer token (empty until Authenticate/SetToken).
func (c *Client) Token() string { return c.token }

// do executes an Infisical REST call at path (joined to baseURL) and returns the
// raw body and status code.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("infisical: marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("infisical: build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("infisical: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("infisical: read response: %w", err)
	}
	return data, resp.StatusCode, nil
}

// Authenticate obtains an access token via Universal Auth and stores it on the
// Client. It refuses a non-loopback http:// base URL: the client secret (the
// org-admin deploy credential) and the returned token travel on this hop, so a
// cleartext endpoint could leak them — but a loopback address cannot be
// intercepted off-host, so http://localhost (or a 127.0.0.0/8 / ::1 IP) is
// allowed for local integration testing against a self-hosted instance.
func (c *Client) Authenticate(ctx context.Context, clientID, clientSecret string) error {
	if err := requireSecureURL(c.baseURL); err != nil {
		return err
	}
	body := map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
	}
	data, status, err := c.do(ctx, http.MethodPost, "/api/v1/auth/universal-auth/login", body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("infisical: authenticate failed (HTTP %d): %s", status, data)
	}
	var resp struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("infisical: parse auth response: %w", err)
	}
	if resp.AccessToken == "" {
		return fmt.Errorf("infisical: empty access token in auth response")
	}
	c.token = resp.AccessToken
	return nil
}

// requireSecureURL permits https, or http only when the host is loopback. The
// host is parsed and matched exactly (localhost, or an IP whose IsLoopback is
// true) — never a substring, so "https://localhost.evil.com" is not treated as
// loopback.
func requireSecureURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("infisical: invalid site URL %q: %w", raw, err)
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("infisical: site URL must be https (got %q)", raw)
}

// orgIDFromToken extracts the organizationId claim from the JWT payload of an
// Infisical universal-auth access token, avoiding a separate API call to the
// organizations endpoint which is not available on all Infisical deployments.
func orgIDFromToken(token string) (string, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("infisical: malformed JWT token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("infisical: decode JWT payload: %w", err)
	}
	var claims struct {
		OrganizationId string `json:"organizationId"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("infisical: parse JWT claims: %w", err)
	}
	if claims.OrganizationId == "" {
		return "", fmt.Errorf("infisical: no organizationId in JWT claims")
	}
	return claims.OrganizationId, nil
}
