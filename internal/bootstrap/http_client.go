package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// HTTPKeyBrokerClient registers keys with the inforge key broker over HTTPS.
// It authenticates via a GitHub Actions OIDC JWT passed in the Authorization
// header. The key broker validates only the GitHub issuer claim; the repository
// claim in the JWT becomes the tenant for cross-repo isolation.
type HTTPKeyBrokerClient struct {
	baseURL   string
	oidcToken string
	client    *http.Client
}

// NewHTTPKeyBrokerClient returns a client that calls the key broker at baseURL
// authenticated with oidcToken. Pass http.DefaultClient or a custom client.
func NewHTTPKeyBrokerClient(baseURL, oidcToken string, client *http.Client) *HTTPKeyBrokerClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPKeyBrokerClient{baseURL: baseURL, oidcToken: oidcToken, client: client}
}

// Register stores key K under token T with the key broker with the given
// TTL. The tenant is derived from the repository claim inside oidcToken; it is
// not sent separately.
func (c *HTTPKeyBrokerClient) Register(token, key, _ string, ttlSeconds int) error {
	body, err := json.Marshal(map[string]any{
		"token":       token,
		"key":         key,
		"ttl_seconds": ttlSeconds,
	})
	if err != nil {
		return fmt.Errorf("marshal key broker request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, c.baseURL+"/token", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build key broker request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.oidcToken)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("key broker PUT /token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("key broker PUT /token: status %d: %s", resp.StatusCode, b)
	}
	return nil
}
