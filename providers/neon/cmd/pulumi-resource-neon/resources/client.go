// Package resources implements the Pulumi custom resource types for the Neon
// provider binary. All resources share the neonDo HTTP helper defined here.
package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const neonAPIBase = "https://console.neon.tech/api/v2"

// neonDo executes a Neon REST API call and returns the response body. Non-2xx
// responses are returned as an error carrying the HTTP status and body text.
func neonDo(ctx context.Context, method, url, apiKey string, body any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("neon: marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("neon: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("neon: %s %s: %w", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("neon: read response: %w", err)
	}

	return data, resp.StatusCode, nil
}
