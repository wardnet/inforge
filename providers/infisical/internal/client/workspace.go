package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// AdoptOrCreateWorkspace returns the ID of an Infisical project matching name,
// creating it if it does not already exist.
func (c *Client) AdoptOrCreateWorkspace(ctx context.Context, name string) (string, error) {
	id, found, err := c.findWorkspace(ctx, name)
	if err != nil {
		return "", err
	}
	if found {
		return id, nil
	}
	return c.createWorkspace(ctx, name)
}

// WorkspaceID returns the ID of the project matching name, erroring if none
// exists. Unlike AdoptOrCreateWorkspace it never creates — callers that must not
// own workspace lifecycle (e.g. `inforge pki renew`, which only writes into a
// workspace deploy already provisioned) use this so a missing workspace surfaces
// as "deploy first" rather than being silently created without an identity.
func (c *Client) WorkspaceID(ctx context.Context, name string) (string, error) {
	id, found, err := c.findWorkspace(ctx, name)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("infisical: no workspace named %q — run `inforge deploy` for this environment first", name)
	}
	return id, nil
}

// findWorkspace looks up a project ID by name, reporting presence.
func (c *Client) findWorkspace(ctx context.Context, name string) (id string, found bool, err error) {
	data, status, err := c.do(ctx, http.MethodGet, "/api/v1/projects", nil)
	if err != nil {
		return "", false, err
	}
	if status < 200 || status >= 300 {
		return "", false, fmt.Errorf("infisical: list projects failed (HTTP %d): %s", status, data)
	}
	var list struct {
		Projects []struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return "", false, fmt.Errorf("infisical: parse projects list: %w", err)
	}
	for _, p := range list.Projects {
		if p.Name == name {
			return p.Id, true, nil
		}
	}
	return "", false, nil
}

func (c *Client) createWorkspace(ctx context.Context, name string) (string, error) {
	body := map[string]any{"projectName": name}
	data, status, err := c.do(ctx, http.MethodPost, "/api/v1/projects", body)
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

// WorkspaceExists reports whether an Infisical project with the given id exists.
// A 404 is a clean "no"; any other non-2xx is an error.
func (c *Client) WorkspaceExists(ctx context.Context, id string) (bool, error) {
	_, status, err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+id, nil)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return false, nil
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("infisical: read workspace %q failed (HTTP %d)", id, status)
	}
	return true, nil
}
