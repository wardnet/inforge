package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/pulumi/pulumi-go-provider/infer"
)

// NeonProjectArgs are the inputs for a Neon project resource.
type NeonProjectArgs struct {
	Name     string `pulumi:"name"`
	RegionId string `pulumi:"regionId"`
	ApiKey   string `pulumi:"apiKey" provider:"secret"`
}

// NeonProjectState is the full state after the resource is created.
type NeonProjectState struct {
	NeonProjectArgs
	ProjectId string `pulumi:"projectId"`
}

// NeonProject manages a Neon project (the account-level container for databases).
// It is idempotent: if a project with the same name and region already exists in
// the account it is adopted rather than re-created.
type NeonProject struct{}

func (*NeonProject) Create(
	ctx context.Context, req infer.CreateRequest[NeonProjectArgs],
) (infer.CreateResponse[NeonProjectState], error) {
	if req.DryRun {
		return infer.CreateResponse[NeonProjectState]{
			ID:     req.Inputs.Name,
			Output: NeonProjectState{NeonProjectArgs: req.Inputs},
		}, nil
	}

	projectId, err := adoptOrCreateProject(ctx, req.Inputs.ApiKey, req.Inputs.Name, req.Inputs.RegionId)
	if err != nil {
		return infer.CreateResponse[NeonProjectState]{}, err
	}
	return infer.CreateResponse[NeonProjectState]{
		ID: projectId,
		Output: NeonProjectState{
			NeonProjectArgs: req.Inputs,
			ProjectId:       projectId,
		},
	}, nil
}

func (*NeonProject) Read(
	ctx context.Context, req infer.ReadRequest[NeonProjectArgs, NeonProjectState],
) (infer.ReadResponse[NeonProjectArgs, NeonProjectState], error) {
	url := fmt.Sprintf("%s/projects/%s", neonAPIBase, req.ID)
	_, status, err := neonDo(ctx, http.MethodGet, url, req.Inputs.ApiKey, nil)
	if err != nil {
		return infer.ReadResponse[NeonProjectArgs, NeonProjectState]{}, err
	}
	if status == http.StatusNotFound {
		return infer.ReadResponse[NeonProjectArgs, NeonProjectState]{}, nil
	}
	if status < 200 || status >= 300 {
		return infer.ReadResponse[NeonProjectArgs, NeonProjectState]{},
			fmt.Errorf("neon: read project %q failed (HTTP %d)", req.ID, status)
	}
	return infer.ReadResponse[NeonProjectArgs, NeonProjectState](req), nil
}

func (*NeonProject) Delete(
	ctx context.Context, req infer.DeleteRequest[NeonProjectState],
) (infer.DeleteResponse, error) {
	url := fmt.Sprintf("%s/projects/%s", neonAPIBase, req.ID)
	_, status, err := neonDo(ctx, http.MethodDelete, url, req.State.ApiKey, nil)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	if status != http.StatusNotFound && (status < 200 || status >= 300) {
		return infer.DeleteResponse{}, fmt.Errorf("neon: delete project %q failed (HTTP %d)", req.ID, status)
	}
	return infer.DeleteResponse{}, nil
}

// adoptOrCreateProject returns the ID of the Neon project with the given name and
// region, creating it if it does not already exist. Adoption prevents duplicate
// projects when a stack is re-deployed after a partial failure.
func adoptOrCreateProject(ctx context.Context, apiKey, name, regionId string) (string, error) {
	listURL := fmt.Sprintf("%s/projects", neonAPIBase)
	data, status, err := neonDo(ctx, http.MethodGet, listURL, apiKey, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("neon: list projects failed (HTTP %d): %s", status, data)
	}

	var list struct {
		Projects []struct {
			Id       string `json:"id"`
			Name     string `json:"name"`
			RegionId string `json:"region_id"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return "", fmt.Errorf("neon: parse projects list: %w", err)
	}
	for _, p := range list.Projects {
		if p.Name == name && p.RegionId == regionId {
			return p.Id, nil
		}
	}

	return createProject(ctx, apiKey, name, regionId)
}

func createProject(ctx context.Context, apiKey, name, regionId string) (string, error) {
	body := map[string]any{
		"project": map[string]any{
			"name":      name,
			"region_id": regionId,
		},
	}
	url := fmt.Sprintf("%s/projects", neonAPIBase)
	data, status, err := neonDo(ctx, http.MethodPost, url, apiKey, body)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("neon: create project %q failed (HTTP %d): %s", name, status, data)
	}

	var resp struct {
		Project struct {
			Id string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("neon: parse create project response: %w", err)
	}
	return resp.Project.Id, nil
}
