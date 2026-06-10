package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// IdentityParams describes a per-service machine identity to provision.
type IdentityParams struct {
	// Name is the identity's display name; adoption is by this name so re-runs
	// reuse the same identity rather than creating duplicates.
	Name string
	// WorkspaceID is the Infisical project the identity is granted scoped read on
	// and added to as a member.
	WorkspaceID string
	// EnvSlug is the environment the read privilege is scoped to (e.g. "prod").
	EnvSlug string
	// SecretPath is the absolute folder the identity may read (e.g. "/ghost").
	SecretPath string
	// OrganizationID pins the org to provision under; empty falls back to the
	// organizationId claim in the authenticated token's JWT.
	OrganizationID string
}

// IdentityCredentials is the result of provisioning: the identity's id plus the
// minted universal-auth credentials the bootstrapper authenticates with.
type IdentityCredentials struct {
	IdentityID       string
	AuthClientID     string
	AuthClientSecret string
}

// ProvisionIdentity runs the adopt-or-create chain for a per-service identity
// scoped read-only to one secret path: adopt-or-create the org identity, attach
// universal auth, add it to the project, grant a read privilege, then mint a
// client secret. The Client must already be Authenticated with an org-admin
// token. Every step is idempotent EXCEPT the final mint, which is one-shot —
// callers must invoke ProvisionIdentity once and persist the result, since it
// mints a fresh secret on every call.
func (c *Client) ProvisionIdentity(ctx context.Context, p IdentityParams) (IdentityCredentials, error) {
	orgID, err := c.resolveOrgID(p.OrganizationID)
	if err != nil {
		return IdentityCredentials{}, err
	}
	identityID, err := c.AdoptOrCreateIdentity(ctx, orgID, p.Name)
	if err != nil {
		return IdentityCredentials{}, err
	}
	authClientID, err := c.EnsureUniversalAuth(ctx, identityID)
	if err != nil {
		return IdentityCredentials{}, err
	}
	if err := c.EnsureProjectMembership(ctx, p.WorkspaceID, identityID); err != nil {
		return IdentityCredentials{}, err
	}
	if err := c.EnsureReadPrivilege(ctx, p.WorkspaceID, identityID, p.EnvSlug, p.SecretPath); err != nil {
		return IdentityCredentials{}, err
	}
	authClientSecret, err := c.MintClientSecret(ctx, identityID)
	if err != nil {
		return IdentityCredentials{}, err
	}
	return IdentityCredentials{
		IdentityID:       identityID,
		AuthClientID:     authClientID,
		AuthClientSecret: authClientSecret,
	}, nil
}

// resolveOrgID prefers an explicitly-configured organization id and falls back
// to the organizationId claim in the access token's JWT. The explicit knob
// exists because not every Infisical deployment's universal-auth token carries
// that claim — Infisical Cloud tokens, in particular, do not.
func (c *Client) resolveOrgID(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return orgIDFromToken(c.token)
}

// AdoptOrCreateIdentity returns the ID of the org identity named name, creating
// it with role no-access if it does not already exist.
func (c *Client) AdoptOrCreateIdentity(ctx context.Context, orgID, name string) (string, error) {
	// limit is set high so adoption sees every identity in one page (max 20000);
	// without it a large org could paginate, miss the existing identity, and
	// create a duplicate on every deploy. This is the v2 org identity-memberships
	// route — the v1 path does not exist and 404s ("Route not found").
	path := "/api/v2/organizations/" + orgID + "/identity-memberships?limit=10000"
	data, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: list identity memberships failed (HTTP %d): %s", status, data)
	}
	var list struct {
		IdentityMemberships []struct {
			Identity struct {
				Id   string `json:"id"`
				Name string `json:"name"`
			} `json:"identity"`
		} `json:"identityMemberships"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return "", fmt.Errorf("infisical: parse identity memberships: %w", err)
	}
	for _, m := range list.IdentityMemberships {
		if m.Identity.Name == name {
			return m.Identity.Id, nil
		}
	}

	createBody := map[string]any{
		"name":           name,
		"organizationId": orgID,
		"role":           "no-access",
	}
	data, status, err = c.do(ctx, http.MethodPost, "/api/v1/identities", createBody)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: create identity %q failed (HTTP %d): %s", name, status, data)
	}
	var resp struct {
		Identity struct {
			Id string `json:"id"`
		} `json:"identity"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("infisical: parse create identity response: %w", err)
	}
	if resp.Identity.Id == "" {
		return "", fmt.Errorf("infisical: create identity %q returned empty id", name)
	}
	return resp.Identity.Id, nil
}

// EnsureUniversalAuth returns the identity's universal-auth clientId, attaching
// universal auth first if it is not already configured. It reads before writing
// (rather than POST-then-tolerate-conflict) so a genuine attach failure — bad
// permissions, server error — surfaces as an error instead of silently falling
// through to whatever the read returns.
func (c *Client) EnsureUniversalAuth(ctx context.Context, identityID string) (string, error) {
	path := "/api/v1/auth/universal-auth/identities/" + identityID

	data, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	if status >= 200 && status < 300 {
		return parseUniversalAuthClientID(data) // already configured
	}
	if status != http.StatusNotFound {
		return "", fmt.Errorf("infisical: read universal auth failed (HTTP %d)", status)
	}

	data, status, err = c.do(ctx, http.MethodPost, path, map[string]any{})
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: attach universal auth failed (HTTP %d): %s", status, data)
	}
	return parseUniversalAuthClientID(data)
}

func parseUniversalAuthClientID(data []byte) (string, error) {
	var resp struct {
		IdentityUniversalAuth struct {
			ClientId string `json:"clientId"`
		} `json:"identityUniversalAuth"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("infisical: parse universal auth response: %w", err)
	}
	if resp.IdentityUniversalAuth.ClientId == "" {
		return "", fmt.Errorf("infisical: empty universal auth clientId")
	}
	return resp.IdentityUniversalAuth.ClientId, nil
}

// EnsureProjectMembership adds the identity to the project as no-access (its read
// scope comes from the additional privilege). It checks membership first and
// creates only when absent, so a real create error is never mistaken for an
// already-member case — a swallowed failure here would leave the identity
// unscoped and surface only as a runtime 403.
func (c *Client) EnsureProjectMembership(ctx context.Context, projectID, identityID string) error {
	path := "/api/v1/projects/" + projectID + "/memberships/identities/" + identityID

	_, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil // already a member
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("infisical: check identity project membership failed (HTTP %d)", status)
	}

	data, status, err := c.do(ctx, http.MethodPost, path, map[string]any{"role": "no-access"})
	if err != nil {
		return err
	}
	// Tolerate only a genuine already-exists conflict (a TOCTOU with a concurrent
	// run); any other non-2xx is a real failure.
	if status == http.StatusConflict || (status >= 200 && status < 300) {
		return nil
	}
	return fmt.Errorf("infisical: add identity to project failed (HTTP %d): %s", status, data)
}

// EnsureReadPrivilege grants the identity read access to secretPath within the
// project's environment. The slug is derived per secretPath so each service's
// privilege is distinct within the shared project (broadcast semantics put
// multiple services in one project) — a fixed slug would collide. Only a genuine
// already-exists conflict is tolerated; any other non-2xx (e.g. a malformed
// permission body) fails loudly at deploy rather than silently leaving the
// identity without a read grant.
func (c *Client) EnsureReadPrivilege(ctx context.Context, projectID, identityID, envSlug, secretPath string) error {
	body := map[string]any{
		"identityId": identityID,
		"projectId":  projectID,
		"slug":       privilegeSlug(secretPath),
		// type is a required discriminated union on isTemporary; a permanent grant
		// is {isTemporary: false}. The API rejects the request (HTTP 422, "type
		// Required") without it — the field is mandatory even though the public API
		// docs omit it.
		"type": map[string]any{"isTemporary": false},
		"permissions": []map[string]any{
			{
				"subject": "secrets",
				"action":  "read",
				"conditions": map[string]any{
					"environment": envSlug,
					"secretPath":  secretPath,
				},
			},
		},
	}
	data, status, err := c.do(ctx, http.MethodPost, "/api/v2/identity-project-additional-privilege", body)
	if err != nil {
		return err
	}
	if status == http.StatusConflict || (status >= 200 && status < 300) {
		return nil
	}
	// On a re-run the privilege already exists, which Infisical reports as HTTP
	// 400 (not 409) with an "...exists" message. That is the desired end state,
	// not a failure — but a genuine bad request (e.g. a malformed permission
	// body) is also a 400, so tolerate only the already-exists message and let
	// every other 400 fail loudly.
	if status == http.StatusBadRequest && bytes.Contains(data, []byte("exists")) {
		return nil
	}
	return fmt.Errorf("infisical: grant read privilege failed (HTTP %d): %s", status, data)
}

// privilegeSlug builds a project-unique, slug-safe identifier for a service's
// read privilege from its secret path (e.g. "/ghost" -> "inforge-read-ghost").
func privilegeSlug(secretPath string) string {
	s := strings.Trim(secretPath, "/")
	s = strings.ReplaceAll(s, "/", "-")
	if s == "" {
		s = "root"
	}
	slug := "inforge-read-" + s
	if len(slug) > 60 {
		slug = slug[:60]
	}
	return slug
}

// MintClientSecret creates a non-expiring, unlimited-use client secret for the
// identity's universal auth and returns the plaintext value (returned only once
// by Infisical).
func (c *Client) MintClientSecret(ctx context.Context, identityID string) (string, error) {
	path := "/api/v1/auth/universal-auth/identities/" + identityID + "/client-secrets"
	body := map[string]any{
		"description":  "inforge-managed",
		"ttl":          0,
		"numUsesLimit": 0,
	}
	data, status, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: mint client secret failed (HTTP %d): %s", status, data)
	}
	var resp struct {
		ClientSecret string `json:"clientSecret"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("infisical: parse client secret response: %w", err)
	}
	if resp.ClientSecret == "" {
		return "", fmt.Errorf("infisical: empty client secret in mint response")
	}
	return resp.ClientSecret, nil
}
