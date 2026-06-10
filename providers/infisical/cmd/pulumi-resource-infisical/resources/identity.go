package resources

import (
	"context"
	"log"

	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/wardnet/inforge/providers/infisical/internal/client"
)

// InfisicalIdentityArgs are the inputs for a per-service Infisical machine
// identity. ClientId/ClientSecret are the deploy (org-admin) credentials used to
// provision the identity; the identity it mints is scoped read-only to
// SecretPath and is what the on-host bootstrapper logs in with.
type InfisicalIdentityArgs struct {
	// Name is the identity's display name; adoption is by this name so re-runs
	// reuse the same identity rather than creating duplicates.
	Name string `pulumi:"name"`
	// WorkspaceId is the Infisical project the identity is granted scoped read on.
	WorkspaceId string `pulumi:"workspaceId"`
	// EnvSlug is the environment the read privilege is scoped to (e.g. "prod").
	EnvSlug string `pulumi:"envSlug"`
	// SecretPath is the absolute folder the identity may read (e.g. "/ghost").
	SecretPath string `pulumi:"secretPath"`
	// ClientId/ClientSecret/SiteUrl are the org-admin deploy credentials.
	ClientId     string `pulumi:"clientId"`
	ClientSecret string `pulumi:"clientSecret" provider:"secret"`
	SiteUrl      string `pulumi:"siteUrl"`
	// OrganizationId optionally pins the Infisical organization to provision the
	// identity under. When empty it is derived from the access token's JWT; it
	// must be set for deployments whose universal-auth token carries no
	// organizationId claim.
	OrganizationId string `pulumi:"organizationId,optional"`
}

// InfisicalIdentityState is the state after the identity is provisioned. It
// carries the minted universal-auth credentials the bootstrapper authenticates
// with; AuthClientSecret is sensitive and is minted exactly once (on Create) and
// thereafter returned unchanged from Read, so a refresh never rotates it.
type InfisicalIdentityState struct {
	InfisicalIdentityArgs
	IdentityId       string `pulumi:"identityId"`
	AuthClientId     string `pulumi:"authClientId"`
	AuthClientSecret string `pulumi:"authClientSecret" provider:"secret"`
}

// InfisicalIdentity provisions a per-service Infisical machine identity scoped
// read-only to a single secret path (see client.ProvisionIdentity for the
// chain). Minting is one-shot, so the minted credential is persisted in state on
// Create and never re-minted on refresh. Identities are never deleted on destroy
// (no-delete policy), matching the workspace/secrets resources.
type InfisicalIdentity struct{}

func (*InfisicalIdentity) Create(
	ctx context.Context, req infer.CreateRequest[InfisicalIdentityArgs],
) (infer.CreateResponse[InfisicalIdentityState], error) {
	if req.DryRun {
		return infer.CreateResponse[InfisicalIdentityState]{
			ID:     req.Inputs.Name,
			Output: InfisicalIdentityState{InfisicalIdentityArgs: req.Inputs},
		}, nil
	}

	in := req.Inputs
	c := client.New(in.SiteUrl)
	if err := c.Authenticate(ctx, in.ClientId, in.ClientSecret); err != nil {
		return infer.CreateResponse[InfisicalIdentityState]{}, err
	}

	creds, err := c.ProvisionIdentity(ctx, client.IdentityParams{
		Name:           in.Name,
		WorkspaceID:    in.WorkspaceId,
		EnvSlug:        in.EnvSlug,
		SecretPath:     in.SecretPath,
		OrganizationID: in.OrganizationId,
	})
	if err != nil {
		return infer.CreateResponse[InfisicalIdentityState]{}, err
	}

	return infer.CreateResponse[InfisicalIdentityState]{
		ID: creds.IdentityID,
		Output: InfisicalIdentityState{
			InfisicalIdentityArgs: in,
			IdentityId:            creds.IdentityID,
			AuthClientId:          creds.AuthClientID,
			AuthClientSecret:      creds.AuthClientSecret,
		},
	}, nil
}

func (*InfisicalIdentity) Read(
	_ context.Context, req infer.ReadRequest[InfisicalIdentityArgs, InfisicalIdentityState],
) (infer.ReadResponse[InfisicalIdentityArgs, InfisicalIdentityState], error) {
	// Return state unchanged: the minted client secret cannot be re-read from
	// Infisical (only its prefix is retrievable), so refresh must preserve it
	// rather than attempt a re-mint.
	return infer.ReadResponse[InfisicalIdentityArgs, InfisicalIdentityState](req), nil
}

func (*InfisicalIdentity) Delete(
	_ context.Context, req infer.DeleteRequest[InfisicalIdentityState],
) (infer.DeleteResponse, error) {
	// Deliberate no-op: identities are never deleted on destroy (no-delete policy).
	log.Printf("infisical: skipping delete of identity %q (no-delete policy)\n", req.ID)
	return infer.DeleteResponse{}, nil
}
