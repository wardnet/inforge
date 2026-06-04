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
	"time"
)

const neonAPIBase = "https://console.neon.tech/api/v2"

// neonDo executes a Neon REST API call and returns the response body. HTTP 423
// (project locked) is retried up to 3 times with exponential backoff because
// Neon serialises concurrent operations on a project.
func neonDo(ctx context.Context, method, url, apiKey string, body any) ([]byte, int, error) {
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("neon: marshal request: %w", err)
		}
	}

	backoff := 5 * time.Second
	for attempt := range 4 {
		var reqBody io.Reader
		if encoded != nil {
			reqBody = bytes.NewReader(encoded)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return nil, 0, fmt.Errorf("neon: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")
		if encoded != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("neon: %s %s: %w", method, url, err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("neon: read response: %w", err)
		}

		if resp.StatusCode == http.StatusLocked && attempt < 3 {
			select {
			case <-ctx.Done():
				return nil, resp.StatusCode, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}

		return data, resp.StatusCode, nil
	}
	panic("unreachable")
}
