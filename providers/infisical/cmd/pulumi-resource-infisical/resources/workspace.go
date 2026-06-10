package resources

import (
	"context"
	"log"

	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/wardnet/inforge/providers/infisical/internal/client"
)

// InfisicalWorkspaceArgs are the inputs for an Infisical workspace resource.
type InfisicalWorkspaceArgs struct {
	Name         string `pulumi:"name"`
	ClientId     string `pulumi:"clientId"`
	ClientSecret string `pulumi:"clientSecret" provider:"secret"`
	SiteUrl      string `pulumi:"siteUrl"`
}

// InfisicalWorkspaceState is the full state after the resource is created.
type InfisicalWorkspaceState struct {
	InfisicalWorkspaceArgs
	WorkspaceId string `pulumi:"workspaceId"`
}

// InfisicalWorkspace manages an Infisical project/workspace. It is idempotent:
// if a workspace with the same name already exists it is adopted rather than
// re-created. Workspaces are never deleted on destroy to prevent accidental
// data loss.
type InfisicalWorkspace struct{}

func (*InfisicalWorkspace) Create(
	ctx context.Context, req infer.CreateRequest[InfisicalWorkspaceArgs],
) (infer.CreateResponse[InfisicalWorkspaceState], error) {
	if req.DryRun {
		return infer.CreateResponse[InfisicalWorkspaceState]{
			ID:     req.Inputs.Name,
			Output: InfisicalWorkspaceState{InfisicalWorkspaceArgs: req.Inputs},
		}, nil
	}

	c := client.New(req.Inputs.SiteUrl)
	if err := c.Authenticate(ctx, req.Inputs.ClientId, req.Inputs.ClientSecret); err != nil {
		return infer.CreateResponse[InfisicalWorkspaceState]{}, err
	}
	workspaceID, err := c.AdoptOrCreateWorkspace(ctx, req.Inputs.Name)
	if err != nil {
		return infer.CreateResponse[InfisicalWorkspaceState]{}, err
	}

	return infer.CreateResponse[InfisicalWorkspaceState]{
		ID: workspaceID,
		Output: InfisicalWorkspaceState{
			InfisicalWorkspaceArgs: req.Inputs,
			WorkspaceId:            workspaceID,
		},
	}, nil
}

func (*InfisicalWorkspace) Read(
	ctx context.Context, req infer.ReadRequest[InfisicalWorkspaceArgs, InfisicalWorkspaceState],
) (infer.ReadResponse[InfisicalWorkspaceArgs, InfisicalWorkspaceState], error) {
	c := client.New(req.State.SiteUrl)
	if err := c.Authenticate(ctx, req.State.ClientId, req.State.ClientSecret); err != nil {
		// Auth failure is treated the same as not found — allows the workspace
		// to be re-adopted on next create rather than blocking refreshes.
		return infer.ReadResponse[InfisicalWorkspaceArgs, InfisicalWorkspaceState]{}, nil
	}

	exists, err := c.WorkspaceExists(ctx, req.ID)
	if err != nil {
		return infer.ReadResponse[InfisicalWorkspaceArgs, InfisicalWorkspaceState]{}, err
	}
	if !exists {
		return infer.ReadResponse[InfisicalWorkspaceArgs, InfisicalWorkspaceState]{}, nil
	}
	return infer.ReadResponse[InfisicalWorkspaceArgs, InfisicalWorkspaceState](req), nil
}

func (*InfisicalWorkspace) Delete(
	_ context.Context, req infer.DeleteRequest[InfisicalWorkspaceState],
) (infer.DeleteResponse, error) {
	// Deliberate no-op: workspaces are never deleted to prevent accidental data loss.
	log.Printf("infisical: skipping delete of workspace %q (no-delete policy)\n", req.ID)
	return infer.DeleteResponse{}, nil
}
