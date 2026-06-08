package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/pulumi/pulumi-go-provider/infer"
)

// NeonDatabaseArgs are the inputs for a Neon branch + database + owner-role resource.
type NeonDatabaseArgs struct {
	ProjectId string `pulumi:"projectId"`
	Branch    string `pulumi:"branch"`
	Database  string `pulumi:"database"`
	Owner     string `pulumi:"owner"`
	ApiKey    string `pulumi:"apiKey" provider:"secret"`
}

// NeonDatabaseState is the full state after the resource is created.
type NeonDatabaseState struct {
	NeonDatabaseArgs
	BranchId      string `pulumi:"branchId"`
	ConnectionUrl string `pulumi:"connectionUrl" provider:"secret"`
}

// NeonDatabase manages a Neon branch plus the role and database created within it.
// The branch is never the primary ("main") branch — deleting this resource deletes
// the branch entirely (roles and databases inside it are removed automatically).
type NeonDatabase struct{}

func (*NeonDatabase) Create(
	ctx context.Context, req infer.CreateRequest[NeonDatabaseArgs],
) (infer.CreateResponse[NeonDatabaseState], error) {
	if req.DryRun {
		return infer.CreateResponse[NeonDatabaseState]{
			ID:     req.Inputs.ProjectId + "/" + req.Inputs.Branch,
			Output: NeonDatabaseState{NeonDatabaseArgs: req.Inputs},
		}, nil
	}

	inp := req.Inputs
	branchId, err := findOrCreateBranch(ctx, inp.ApiKey, inp.ProjectId, inp.Branch)
	if err != nil {
		return infer.CreateResponse[NeonDatabaseState]{}, err
	}
	if err := ensureRole(ctx, inp.ApiKey, inp.ProjectId, branchId, inp.Owner); err != nil {
		return infer.CreateResponse[NeonDatabaseState]{}, err
	}
	if err := ensureDatabase(ctx, inp.ApiKey, inp.ProjectId, branchId, inp.Database, inp.Owner); err != nil {
		return infer.CreateResponse[NeonDatabaseState]{}, err
	}
	connURL, err := getConnectionURI(ctx, inp.ApiKey, inp.ProjectId, branchId, inp.Owner, inp.Database)
	if err != nil {
		return infer.CreateResponse[NeonDatabaseState]{}, err
	}

	return infer.CreateResponse[NeonDatabaseState]{
		ID: branchId,
		Output: NeonDatabaseState{
			NeonDatabaseArgs: inp,
			BranchId:         branchId,
			ConnectionUrl:    connURL,
		},
	}, nil
}

func (*NeonDatabase) Read(
	ctx context.Context, req infer.ReadRequest[NeonDatabaseArgs, NeonDatabaseState],
) (infer.ReadResponse[NeonDatabaseArgs, NeonDatabaseState], error) {
	url := fmt.Sprintf("%s/projects/%s/branches/%s", neonAPIBase, req.Inputs.ProjectId, req.ID)
	_, status, err := neonDo(ctx, http.MethodGet, url, req.Inputs.ApiKey, nil)
	if err != nil {
		return infer.ReadResponse[NeonDatabaseArgs, NeonDatabaseState]{}, err
	}
	if status == http.StatusNotFound {
		return infer.ReadResponse[NeonDatabaseArgs, NeonDatabaseState]{}, nil
	}
	if status < 200 || status >= 300 {
		return infer.ReadResponse[NeonDatabaseArgs, NeonDatabaseState]{},
			fmt.Errorf("neon: read branch %q failed (HTTP %d)", req.ID, status)
	}
	return infer.ReadResponse[NeonDatabaseArgs, NeonDatabaseState](req), nil
}

func (*NeonDatabase) Delete(
	ctx context.Context, req infer.DeleteRequest[NeonDatabaseState],
) (infer.DeleteResponse, error) {
	branchId := req.ID
	if branchId == "main" || req.State.Branch == "main" {
		return infer.DeleteResponse{}, fmt.Errorf("neon: refusing to delete the main branch of project %q", req.State.ProjectId)
	}

	url := fmt.Sprintf("%s/projects/%s/branches/%s", neonAPIBase, req.State.ProjectId, branchId)
	_, status, err := neonDo(ctx, http.MethodDelete, url, req.State.ApiKey, nil)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	if status != http.StatusNotFound && (status < 200 || status >= 300) {
		return infer.DeleteResponse{}, fmt.Errorf("neon: delete branch %q failed (HTTP %d)", branchId, status)
	}
	return infer.DeleteResponse{}, nil
}

// findOrCreateBranch returns the branch ID for the given branch name in the project,
// creating it if it does not yet exist.
func findOrCreateBranch(ctx context.Context, apiKey, projectId, branchName string) (string, error) {
	listURL := fmt.Sprintf("%s/projects/%s/branches", neonAPIBase, projectId)
	data, status, err := neonDo(ctx, http.MethodGet, listURL, apiKey, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("neon: list branches for project %q failed (HTTP %d): %s", projectId, status, data)
	}

	var list struct {
		Branches []struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return "", fmt.Errorf("neon: parse branches list: %w", err)
	}
	for _, b := range list.Branches {
		if b.Name == branchName {
			return b.Id, nil
		}
	}

	return createBranch(ctx, apiKey, projectId, branchName)
}

func createBranch(ctx context.Context, apiKey, projectId, branchName string) (string, error) {
	body := map[string]any{
		"branch": map[string]any{"name": branchName},
		"endpoints": []map[string]any{
			{"type": "read_write"},
		},
	}
	url := fmt.Sprintf("%s/projects/%s/branches", neonAPIBase, projectId)
	data, status, err := neonDo(ctx, http.MethodPost, url, apiKey, body)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("neon: create branch %q failed (HTTP %d): %s", branchName, status, data)
	}

	var resp struct {
		Branch struct {
			Id string `json:"id"`
		} `json:"branch"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("neon: parse create branch response: %w", err)
	}
	return resp.Branch.Id, nil
}

// ensureRole creates the role; HTTP 409 (conflict) means it already exists and is ignored.
func ensureRole(ctx context.Context, apiKey, projectId, branchId, roleName string) error {
	body := map[string]any{"role": map[string]any{"name": roleName}}
	url := fmt.Sprintf("%s/projects/%s/branches/%s/roles", neonAPIBase, projectId, branchId)
	_, status, err := neonDo(ctx, http.MethodPost, url, apiKey, body)
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		return nil
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("neon: create role %q failed (HTTP %d)", roleName, status)
	}
	return nil
}

// ensureDatabase creates the database; HTTP 409 (conflict) means it already exists and is ignored.
func ensureDatabase(ctx context.Context, apiKey, projectId, branchId, dbName, ownerRole string) error {
	body := map[string]any{
		"database": map[string]any{
			"name":       dbName,
			"owner_name": ownerRole,
		},
	}
	url := fmt.Sprintf("%s/projects/%s/branches/%s/databases", neonAPIBase, projectId, branchId)
	_, status, err := neonDo(ctx, http.MethodPost, url, apiKey, body)
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		return nil
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("neon: create database %q failed (HTTP %d)", dbName, status)
	}
	return nil
}

func getConnectionURI(ctx context.Context, apiKey, projectId, branchId, roleName, dbName string) (string, error) {
	url := fmt.Sprintf(
		"%s/projects/%s/connection_uri?branch_id=%s&role_name=%s&database_name=%s",
		neonAPIBase, projectId, branchId, roleName, dbName,
	)
	data, status, err := neonDo(ctx, http.MethodGet, url, apiKey, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("neon: get connection URI for project %q branch %q failed (HTTP %d): %s", projectId, branchId, status, data)
	}

	var resp struct {
		Uri string `json:"uri"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("neon: parse connection URI response: %w", err)
	}
	return resp.Uri, nil
}
