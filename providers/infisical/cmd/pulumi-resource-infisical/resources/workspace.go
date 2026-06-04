package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/pulumi/pulumi-go-provider/infer"
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

	token, err := authenticate(ctx, req.Inputs.SiteUrl, req.Inputs.ClientId, req.Inputs.ClientSecret)
	if err != nil {
		return infer.CreateResponse[InfisicalWorkspaceState]{}, err
	}

	workspaceId, err := adoptOrCreateWorkspace(ctx, req.Inputs.SiteUrl, token, req.Inputs.Name)
	if err != nil {
		return infer.CreateResponse[InfisicalWorkspaceState]{}, err
	}

	return infer.CreateResponse[InfisicalWorkspaceState]{
		ID: workspaceId,
		Output: InfisicalWorkspaceState{
			InfisicalWorkspaceArgs: req.Inputs,
			WorkspaceId:            workspaceId,
		},
	}, nil
}

func (*InfisicalWorkspace) Read(
	ctx context.Context, req infer.ReadRequest[InfisicalWorkspaceArgs, InfisicalWorkspaceState],
) (infer.ReadResponse[InfisicalWorkspaceArgs, InfisicalWorkspaceState], error) {
	token, err := authenticate(ctx, req.State.SiteUrl, req.State.ClientId, req.State.ClientSecret)
	if err != nil {
		// Auth failure is treated the same as not found — allows the workspace
		// to be re-adopted on next create rather than blocking refreshes.
		return infer.ReadResponse[InfisicalWorkspaceArgs, InfisicalWorkspaceState]{}, nil
	}

	url := req.State.SiteUrl + "/api/v1/projects/" + req.ID
	_, status, err := infisicalDo(ctx, http.MethodGet, url, token, nil)
	if err != nil {
		return infer.ReadResponse[InfisicalWorkspaceArgs, InfisicalWorkspaceState]{}, err
	}
	if status == http.StatusNotFound {
		return infer.ReadResponse[InfisicalWorkspaceArgs, InfisicalWorkspaceState]{}, nil
	}
	if status < 200 || status >= 300 {
		return infer.ReadResponse[InfisicalWorkspaceArgs, InfisicalWorkspaceState]{},
			fmt.Errorf("infisical: read workspace %q failed (HTTP %d)", req.ID, status)
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

// adoptOrCreateWorkspace returns the ID of an Infisical project matching name,
// creating it if it does not already exist.
func adoptOrCreateWorkspace(ctx context.Context, siteURL, token, name string) (string, error) {
	listURL := siteURL + "/api/v1/projects"
	data, status, err := infisicalDo(ctx, http.MethodGet, listURL, token, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: list projects failed (HTTP %d): %s", status, data)
	}

	var list struct {
		Projects []struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return "", fmt.Errorf("infisical: parse projects list: %w", err)
	}
	for _, p := range list.Projects {
		if p.Name == name {
			return p.Id, nil
		}
	}

	return createWorkspace(ctx, siteURL, token, name)
}

func createWorkspace(ctx context.Context, siteURL, token, name string) (string, error) {
	url := siteURL + "/api/v1/projects"
	body := map[string]any{
		"projectName": name,
	}
	data, status, err := infisicalDo(ctx, http.MethodPost, url, token, body)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: create project %q failed (HTTP %d): %s", name, status, data)
	}

	var resp struct {
		Project struct {
			Id string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("infisical: parse create project response: %w", err)
	}
	return resp.Project.Id, nil
}
