package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpTimeout bounds each Infisical round-trip. Without it a hung connection
// (server accepts, never responds) would block ExecStart indefinitely, since the
// fetch backoff budget is only evaluated between attempts — a stalled attempt
// would never return to be timed out.
const httpTimeout = 30 * time.Second

// Provider holds the secrets-provider coordinates from the descriptor. It is
// shared across providers; Kind selects the SecretsFetcher implementation.
type Provider struct {
	Kind        string `yaml:"kind"`
	URL         string `yaml:"url"`
	Project     string `yaml:"project"`
	Environment string `yaml:"environment"`
	SecretPath  string `yaml:"secret_path"`
}

// infisicalCredential is the decrypted client credential for a per-service
// Infisical machine identity. inforge delivers it host-key-encrypted; the
// agent logs in with it to obtain a fresh short-lived access token at each
// start. The standing on-host secret is this rotatable identity secret, never a
// secret value.
type infisicalCredential struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// infisicalFetcher is the Infisical SecretsFetcher: universal-auth login, then a
// single recursive raw-secrets read under the service's secret_path.
type infisicalFetcher struct {
	p            Provider
	clientID     string
	clientSecret string
	http         *http.Client
}

// newInfisicalFetcher parses the decrypted credential blob and builds a fetcher.
func newInfisicalFetcher(p Provider, credential []byte) (*infisicalFetcher, error) {
	var c infisicalCredential
	if err := json.Unmarshal(credential, &c); err != nil {
		return nil, fmt.Errorf("infisical: parse credential: %w", err)
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return nil, fmt.Errorf("infisical: credential missing client_id or client_secret")
	}
	if p.URL == "" || p.Project == "" || p.Environment == "" || p.SecretPath == "" {
		return nil, fmt.Errorf("infisical: provider requires url, project, environment, secret_path")
	}
	// The client_secret and bearer token travel on the first hop, so refuse a
	// non-TLS provider URL — an http:// endpoint would send them in cleartext.
	if !strings.HasPrefix(p.URL, "https://") {
		return nil, fmt.Errorf("infisical: provider url must be https, got %q", p.URL)
	}
	return &infisicalFetcher{
		p:            p,
		clientID:     c.ClientID,
		clientSecret: c.ClientSecret,
		http:         &http.Client{Timeout: httpTimeout},
	}, nil
}

// Fetch logs in and returns the service's secrets keyed by "<relpath>/<key>".
func (f *infisicalFetcher) Fetch(ctx context.Context) (map[string]string, error) {
	token, err := f.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return f.fetchSecrets(ctx, token)
}

// authenticate performs Infisical universal-auth login and returns a short-lived
// access token. Network errors, 429, and 5xx are transient (retry); 401/403 and
// other non-2xx are permanent (fail fast). The response body is never logged —
// the 2xx body carries the access token.
func (f *infisicalFetcher) authenticate(ctx context.Context) (string, error) {
	u := f.p.URL + "/api/v1/auth/universal-auth/login"
	body := map[string]any{"clientId": f.clientID, "clientSecret": f.clientSecret}

	data, status, err := infisicalDo(ctx, f.http, http.MethodPost, u, "", body)
	if err != nil {
		return "", transient("infisical: authenticate: %w", err)
	}
	if status == http.StatusTooManyRequests || status >= 500 {
		return "", transient("infisical: authenticate transient failure (HTTP %d)", status)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: authenticate failed (HTTP %d)", status)
	}

	var resp struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("infisical: parse auth response: %w", err)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("infisical: empty access token in auth response")
	}
	return resp.AccessToken, nil
}

// fetchSecrets reads every secret under secret_path recursively in one
// round-trip and flattens them to "<relpath>/<key>" references. The response
// body is parsed but never logged (it holds secret values).
func (f *infisicalFetcher) fetchSecrets(ctx context.Context, token string) (map[string]string, error) {
	q := url.Values{
		"workspaceId": {f.p.Project},
		"environment": {f.p.Environment},
		"secretPath":  {f.p.SecretPath},
		"recursive":   {"true"},
	}
	u := f.p.URL + "/api/v3/secrets/raw?" + q.Encode()

	data, status, err := infisicalDo(ctx, f.http, http.MethodGet, u, token, nil)
	if err != nil {
		return nil, transient("infisical: fetch secrets: %w", err)
	}
	if status == http.StatusTooManyRequests || status >= 500 {
		return nil, transient("infisical: fetch secrets transient failure (HTTP %d)", status)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("infisical: fetch secrets failed (HTTP %d)", status)
	}

	// Assumed v3 raw response shape: {"secrets":[{secretKey, secretValue,
	// secretPath}, ...]} with secretPath ABSOLUTE and prefixed by the requested
	// secret_path (e.g. "/ghost/infra" under "/ghost"). The flatten below depends
	// on that prefix relationship; it is the primary thing the live E2E must
	// validate against a real Infisical instance (writes use v4 per #54; v3 raw is
	// the documented read endpoint).
	var resp struct {
		Secrets []struct {
			SecretKey   string `json:"secretKey"`
			SecretValue string `json:"secretValue"`
			SecretPath  string `json:"secretPath"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("infisical: parse secrets response: %w", err)
	}

	base := strings.TrimRight(f.p.SecretPath, "/")
	out := make(map[string]string, len(resp.Secrets))
	for _, s := range resp.Secrets {
		// Anchor the prefix strip on a path boundary so a sibling that merely
		// shares a string prefix (e.g. "/ghostly/infra" under "/ghost") is not
		// mis-flattened into a bogus key; skip anything not actually under base.
		var rel string
		switch {
		case s.SecretPath == base:
			rel = ""
		case strings.HasPrefix(s.SecretPath, base+"/"):
			rel = strings.Trim(strings.TrimPrefix(s.SecretPath, base+"/"), "/")
		default:
			continue
		}
		key := s.SecretKey
		if rel != "" {
			key = rel + "/" + key
		}
		out[key] = s.SecretValue
	}
	return out, nil
}

// infisicalDo executes an Infisical REST call and returns the response body and
// status. Copied (deliberately, not imported) from the Infisical provider's
// client.go: the provider package pulls the Pulumi SDK, which the dependency-light
// agent must not link. Keep the two in sync if the REST contract changes.
func infisicalDo(ctx context.Context, client *http.Client, method, reqURL, token string, body any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("infisical: marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("infisical: build request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("infisical: %s %s: %w", method, reqURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("infisical: read response: %w", err)
	}
	return data, resp.StatusCode, nil
}
