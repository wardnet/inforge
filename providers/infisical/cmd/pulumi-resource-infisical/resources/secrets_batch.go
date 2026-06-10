package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/wardnet/inforge/providers/infisical/internal/client"
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

	var secrets map[string]string
	if err := json.Unmarshal([]byte(req.Inputs.SecretsJson), &secrets); err != nil {
		return infer.CreateResponse[InfisicalSecretsBatchState]{},
			fmt.Errorf("infisical: parse secretsJson: %w", err)
	}

	c := client.New(req.Inputs.SiteUrl)
	if err := c.Authenticate(ctx, req.Inputs.ClientId, req.Inputs.ClientSecret); err != nil {
		return infer.CreateResponse[InfisicalSecretsBatchState]{}, err
	}
	if err := c.WriteSecrets(ctx, req.Inputs.WorkspaceId, req.Inputs.EnvSlug, req.Inputs.SecretPath, secrets); err != nil {
		return infer.CreateResponse[InfisicalSecretsBatchState]{}, err
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
