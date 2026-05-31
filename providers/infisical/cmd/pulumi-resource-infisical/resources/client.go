// Package resources implements the Pulumi custom resource types for the
// Infisical provider binary. All resources share the infisicalDo HTTP helper
// and the authenticate helper defined here.
package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// infisicalDo executes an Infisical REST API call and returns the response body.
func infisicalDo(ctx context.Context, method, url, token string, body any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("infisical: marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("infisical: %s %s: %w", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("infisical: read response: %w", err)
	}
	return data, resp.StatusCode, nil
}

// authenticate obtains an Infisical access token via Universal Auth.
func authenticate(ctx context.Context, siteURL, clientID, clientSecret string) (string, error) {
	url := siteURL + "/api/v1/auth/universal-auth/login"
	body := map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
	}
	data, status, err := infisicalDo(ctx, http.MethodPost, url, "", body)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: authenticate failed (HTTP %d): %s", status, data)
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
