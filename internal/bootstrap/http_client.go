package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// HTTPEscrowClient registers keys with the inforge escrow worker over HTTPS.
// It authenticates via a GitHub Actions OIDC JWT passed in the Authorization
// header. The escrow validates only the GitHub issuer claim; the repository
// claim in the JWT becomes the tenant for cross-repo isolation.
type HTTPEscrowClient struct {
	baseURL   string
	oidcToken string
	client    *http.Client
}

// NewHTTPEscrowClient returns a client that calls the escrow at baseURL
// authenticated with oidcToken. Pass http.DefaultClient or a custom client.
func NewHTTPEscrowClient(baseURL, oidcToken string, client *http.Client) *HTTPEscrowClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPEscrowClient{baseURL: baseURL, oidcToken: oidcToken, client: client}
}

// Register stores key K under token T with the escrow worker. The tenant is
// derived from the repository claim inside oidcToken; it is not sent separately.
func (c *HTTPEscrowClient) Register(token, key, _ string) error {
	body, err := json.Marshal(map[string]string{"token": token, "key": key})
	if err != nil {
		return fmt.Errorf("marshal escrow request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, c.baseURL+"/token", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build escrow request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.oidcToken)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("escrow PUT /token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("escrow PUT /token: status %d: %s", resp.StatusCode, b)
	}
	return nil
}
