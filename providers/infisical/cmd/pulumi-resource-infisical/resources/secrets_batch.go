package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/pulumi/pulumi-go-provider/infer"
)

// InfisicalSecretsBatchArgs are the inputs for an Infisical secrets batch resource.
type InfisicalSecretsBatchArgs struct {
	WorkspaceId  string `pulumi:"workspaceId"`
	EnvSlug      string `pulumi:"envSlug"`
	ClientId     string `pulumi:"clientId"`
	ClientSecret string `pulumi:"clientSecret" provider:"secret"`
	SiteUrl      string `pulumi:"siteUrl"`
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

	for key, value := range secrets {
		if err := upsertSecret(ctx, req.Inputs.SiteUrl, token, req.Inputs.WorkspaceId, req.Inputs.EnvSlug, key, value); err != nil {
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

// upsertSecret writes key=value into the workspace+environment, patching on conflict (HTTP 409).
func upsertSecret(ctx context.Context, siteURL, token, workspaceId, envSlug, key, value string) error {
	url := siteURL + "/api/v3/secrets/raw/" + key
	body := map[string]any{
		"workspaceId": workspaceId,
		"environment": envSlug,
		"secretValue": value,
		"type":        "shared",
	}
	data, status, err := infisicalDo(ctx, http.MethodPost, url, token, body)
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		patchBody := map[string]any{
			"workspaceId": workspaceId,
			"environment": envSlug,
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
