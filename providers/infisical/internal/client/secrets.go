package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// WriteSecrets writes a batch of key=value secrets into the project+environment
// at secretPath. It first creates the folder path (Infisical does not create
// folders implicitly on secret write), then upserts each key. The write is N+1
// (one round-trip per key, plus a possible PATCH on conflict): Infisical has no
// public batch upsert endpoint at time of writing.
func (c *Client) WriteSecrets(ctx context.Context, projectID, envSlug, secretPath string, secrets map[string]string) error {
	if err := c.ensureFolderPath(ctx, projectID, envSlug, secretPath); err != nil {
		return err
	}
	for key, value := range secrets {
		if err := c.upsertSecret(ctx, projectID, envSlug, secretPath, key, value); err != nil {
			return err
		}
	}
	return nil
}

// ensureFolderPath creates each folder level in secretPath (e.g. /ghost/infra →
// "ghost" under "/", then "infra" under "/ghost") so a subsequent secret write
// targeting that path succeeds. A root or empty path is a no-op. Re-creating an
// existing folder returns a conflict (HTTP 400/409) which is treated as success,
// keeping this idempotent.
func (c *Client) ensureFolderPath(ctx context.Context, projectID, envSlug, secretPath string) error {
	trimmed := strings.Trim(secretPath, "/")
	if trimmed == "" {
		return nil
	}
	parent := "/"
	for _, name := range strings.Split(trimmed, "/") {
		body := map[string]any{
			"projectId":   projectID,
			"environment": envSlug,
			"name":        name,
			"path":        parent,
		}
		data, status, err := c.do(ctx, http.MethodPost, "/api/v2/folders", body)
		if err != nil {
			return err
		}
		// Already-exists conflicts are expected on re-runs and are not failures.
		if status != http.StatusConflict && status != http.StatusBadRequest && (status < 200 || status >= 300) {
			return fmt.Errorf("infisical: create folder %q under %q failed (HTTP %d): %s", name, parent, status, data)
		}
		if parent == "/" {
			parent = "/" + name
		} else {
			parent += "/" + name
		}
	}
	return nil
}

// upsertSecret writes key=value into the project+environment at secretPath,
// patching on conflict (HTTP 400). An empty secretPath defaults to the workspace
// root. The target folder must already exist (see ensureFolderPath).
func (c *Client) upsertSecret(ctx context.Context, projectID, envSlug, secretPath, key, value string) error {
	if secretPath == "" {
		secretPath = "/"
	}
	path := "/api/v4/secrets/" + key
	body := map[string]any{
		"projectId":   projectID,
		"environment": envSlug,
		"secretPath":  secretPath,
		"secretValue": value,
	}
	data, status, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if status == http.StatusBadRequest {
		data, status, err = c.do(ctx, http.MethodPatch, path, body)
		if err != nil {
			return err
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("infisical: update secret %q failed (HTTP %d): %s", key, status, data)
		}
		return nil
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("infisical: create secret %q failed (HTTP %d): %s", key, status, data)
	}
	return nil
}
