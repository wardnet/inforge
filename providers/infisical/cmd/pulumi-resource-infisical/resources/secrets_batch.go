package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/pulumi/pulumi-go-provider/infer"
)

// InfisicalSecretsBatchArgs are the inputs for an Infisical secrets batch resource.
type InfisicalSecretsBatchArgs struct {
	WorkspaceId  string `pulumi:"workspaceId"`
	EnvSlug      string `pulumi:"envSlug"`
	ClientId     string `pulumi:"clientId"`
	ClientSecret string `pulumi:"clientSecret" provider:"secret"`
	SiteUrl      string `pulumi:"siteUrl"`
	// SecretPath is the absolute Infisical folder the batch writes into (e.g.
	// "/ghost/infra"). Empty means the workspace root ("/"), preserving the
	// historical behaviour. inforge writes per-service infra secrets under
	// "/<svc>/infra" so each service's scoped identity reads only its own path.
	SecretPath string `pulumi:"secretPath"`
	// SecretsJson is a JSON-encoded map[string]string of resolved secret key-value
	// pairs. It must be a secret because it contains resolved output values
	// (e.g. database connection strings).
	SecretsJson string `pulumi:"secretsJson" provider:"secret"`
}

// InfisicalSecretsBatchState is the full state after the resource is created.
type InfisicalSecretsBatchState struct {
	InfisicalSecretsBatchArgs
}

// InfisicalSecretsBatch writes a batch of key-value secrets into an Infisical
// workspace + environment. Secrets are never deleted on destroy (no-delete policy).
type InfisicalSecretsBatch struct{}

func (*InfisicalSecretsBatch) Create(
	ctx context.Context, req infer.CreateRequest[InfisicalSecretsBatchArgs],
) (infer.CreateResponse[InfisicalSecretsBatchState], error) {
	id := req.Inputs.WorkspaceId + "/" + req.Inputs.EnvSlug
	if req.DryRun {
		return infer.CreateResponse[InfisicalSecretsBatchState]{
			ID:     id,
			Output: InfisicalSecretsBatchState{InfisicalSecretsBatchArgs: req.Inputs},
		}, nil
	}

	token, err := authenticate(ctx, req.Inputs.SiteUrl, req.Inputs.ClientId, req.Inputs.ClientSecret)
	if err != nil {
		return infer.CreateResponse[InfisicalSecretsBatchState]{}, err
	}

	var secrets map[string]string
	if err := json.Unmarshal([]byte(req.Inputs.SecretsJson), &secrets); err != nil {
		return infer.CreateResponse[InfisicalSecretsBatchState]{},
			fmt.Errorf("infisical: parse secretsJson: %w", err)
	}

	// Infisical does not auto-create folders on secret write, so the target path
	// (e.g. /ghost/infra) must exist before the upserts below or they 404.
	if err := ensureFolderPath(ctx, req.Inputs.SiteUrl, token, req.Inputs.WorkspaceId, req.Inputs.EnvSlug, req.Inputs.SecretPath); err != nil {
		return infer.CreateResponse[InfisicalSecretsBatchState]{}, err
	}

	// N+1: one HTTP round-trip per secret key (POST + possible PATCH on 409).
	// Infisical has no public batch upsert endpoint at time of writing; revisit
	// if one is added to their API.
	for key, value := range secrets {
		if err := upsertSecret(ctx, req.Inputs.SiteUrl, token, req.Inputs.WorkspaceId, req.Inputs.EnvSlug, req.Inputs.SecretPath, key, value); err != nil {
			return infer.CreateResponse[InfisicalSecretsBatchState]{}, err
		}
	}

	return infer.CreateResponse[InfisicalSecretsBatchState]{
		ID:     id,
		Output: InfisicalSecretsBatchState{InfisicalSecretsBatchArgs: req.Inputs},
	}, nil
}

func (*InfisicalSecretsBatch) Read(
	_ context.Context, req infer.ReadRequest[InfisicalSecretsBatchArgs, InfisicalSecretsBatchState],
) (infer.ReadResponse[InfisicalSecretsBatchArgs, InfisicalSecretsBatchState], error) {
	// Stateless cache: return inputs unchanged.
	return infer.ReadResponse[InfisicalSecretsBatchArgs, InfisicalSecretsBatchState](req), nil
}

func (*InfisicalSecretsBatch) Delete(
	_ context.Context, req infer.DeleteRequest[InfisicalSecretsBatchState],
) (infer.DeleteResponse, error) {
	// Deliberate no-op: secrets are never deleted on destroy to prevent accidental data loss.
	log.Printf("infisical: skipping delete of secrets batch %q (no-delete policy)\n", req.ID)
	return infer.DeleteResponse{}, nil
}

// ensureFolderPath creates each folder level in secretPath (e.g. /ghost/infra →
// "ghost" under "/", then "infra" under "/ghost") so a subsequent secret write
// targeting that path succeeds — Infisical does not create folders implicitly. A
// root or empty path is a no-op. Re-creating an existing folder returns a
// conflict (HTTP 400/409) which is treated as success, keeping this idempotent.
func ensureFolderPath(ctx context.Context, siteURL, token, workspaceId, envSlug, secretPath string) error {
	trimmed := strings.Trim(secretPath, "/")
	if trimmed == "" {
		return nil
	}
	parent := "/"
	for _, name := range strings.Split(trimmed, "/") {
		body := map[string]any{
			"projectId":   workspaceId,
			"environment": envSlug,
			"name":        name,
			"path":        parent,
		}
		data, status, err := infisicalDo(ctx, http.MethodPost, siteURL+"/api/v2/folders", token, body)
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
func upsertSecret(ctx context.Context, siteURL, token, workspaceId, envSlug, secretPath, key, value string) error {
	url := siteURL + "/api/v4/secrets/" + key
	if secretPath == "" {
		secretPath = "/"
	}
	body := map[string]any{
		"projectId":   workspaceId,
		"environment": envSlug,
		"secretPath":  secretPath,
		"secretValue": value,
	}
	data, status, err := infisicalDo(ctx, http.MethodPost, url, token, body)
	if err != nil {
		return err
	}
	if status == http.StatusBadRequest {
		patchBody := map[string]any{
			"projectId":   workspaceId,
			"environment": envSlug,
			"secretPath":  secretPath,
			"secretValue": value,
		}
		data, status, err = infisicalDo(ctx, http.MethodPatch, url, token, patchBody)
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
