package resources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

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

	url := req.State.SiteUrl + "/api/v1/workspace/" + req.ID
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

// adoptOrCreateWorkspace returns the ID of an Infisical workspace matching name,
// creating it if it does not already exist.
func adoptOrCreateWorkspace(ctx context.Context, siteURL, token, name string) (string, error) {
	listURL := siteURL + "/api/v1/workspace"
	data, status, err := infisicalDo(ctx, http.MethodGet, listURL, token, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: list workspaces failed (HTTP %d): %s", status, data)
	}

	var list struct {
		Workspaces []struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return "", fmt.Errorf("infisical: parse workspaces list: %w", err)
	}
	for _, w := range list.Workspaces {
		if w.Name == name {
			return w.Id, nil
		}
	}

	return createWorkspace(ctx, siteURL, token, name)
}

func createWorkspace(ctx context.Context, siteURL, token, name string) (string, error) {
	orgId, err := orgIdFromToken(token)
	if err != nil {
		return "", err
	}

	url := siteURL + "/api/v1/workspace"
	body := map[string]any{
		"workspaceName":  name,
		"organizationId": orgId,
	}
	data, status, err := infisicalDo(ctx, http.MethodPost, url, token, body)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: create workspace %q failed (HTTP %d): %s", name, status, data)
	}

	var resp struct {
		Workspace struct {
			Id string `json:"id"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("infisical: parse create workspace response: %w", err)
	}
	return resp.Workspace.Id, nil
}

// orgIdFromToken extracts the organization ID from the JWT payload of an
// Infisical universal-auth access token, avoiding a separate API call to the
// organizations endpoint which is not available on all Infisical deployments.
func orgIdFromToken(token string) (string, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("infisical: malformed JWT token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("infisical: decode JWT payload: %w", err)
	}
	var claims struct {
		OrganizationId string `json:"organizationId"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("infisical: parse JWT claims: %w", err)
	}
	if claims.OrganizationId == "" {
		return "", fmt.Errorf("infisical: no organizationId in JWT claims")
	}
	return claims.OrganizationId, nil
}
