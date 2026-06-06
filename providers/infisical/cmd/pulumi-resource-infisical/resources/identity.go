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
// read-only to a single secret path: adopt-or-create the org identity, attach
// universal auth, add it to the project, grant a read privilege on SecretPath,
// then mint a client secret. It is idempotent for everything except the client
// secret — minting is inherently one-shot, so the minted credential is persisted
// in state and never re-minted on refresh. Identities are never deleted on
// destroy (no-delete policy), matching the workspace/secrets resources.
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
	token, err := authenticate(ctx, in.SiteUrl, in.ClientId, in.ClientSecret)
	if err != nil {
		return infer.CreateResponse[InfisicalIdentityState]{}, err
	}
	orgId, err := orgIdFromToken(token)
	if err != nil {
		return infer.CreateResponse[InfisicalIdentityState]{}, err
	}

	identityId, err := adoptOrCreateIdentity(ctx, in.SiteUrl, token, orgId, in.Name)
	if err != nil {
		return infer.CreateResponse[InfisicalIdentityState]{}, err
	}
	authClientId, err := ensureUniversalAuth(ctx, in.SiteUrl, token, identityId)
	if err != nil {
		return infer.CreateResponse[InfisicalIdentityState]{}, err
	}
	if err := ensureProjectMembership(ctx, in.SiteUrl, token, in.WorkspaceId, identityId); err != nil {
		return infer.CreateResponse[InfisicalIdentityState]{}, err
	}
	if err := ensureReadPrivilege(ctx, in.SiteUrl, token, in.WorkspaceId, identityId, in.EnvSlug, in.SecretPath); err != nil {
		return infer.CreateResponse[InfisicalIdentityState]{}, err
	}
	authClientSecret, err := mintClientSecret(ctx, in.SiteUrl, token, identityId)
	if err != nil {
		return infer.CreateResponse[InfisicalIdentityState]{}, err
	}

	return infer.CreateResponse[InfisicalIdentityState]{
		ID: identityId,
		Output: InfisicalIdentityState{
			InfisicalIdentityArgs: in,
			IdentityId:            identityId,
			AuthClientId:          authClientId,
			AuthClientSecret:      authClientSecret,
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

// adoptOrCreateIdentity returns the ID of the org identity named name, creating
// it with role no-access if it does not already exist.
func adoptOrCreateIdentity(ctx context.Context, siteURL, token, orgId, name string) (string, error) {
	listURL := siteURL + "/api/v1/organizations/" + orgId + "/identity-memberships"
	data, status, err := infisicalDo(ctx, http.MethodGet, listURL, token, nil)
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
		"organizationId": orgId,
		"role":           "no-access",
	}
	data, status, err = infisicalDo(ctx, http.MethodPost, siteURL+"/api/v1/identities", token, createBody)
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

// ensureUniversalAuth attaches universal auth to the identity and returns its
// clientId. If universal auth is already configured (a re-run), the attach
// conflicts and the existing clientId is fetched instead.
func ensureUniversalAuth(ctx context.Context, siteURL, token, identityId string) (string, error) {
	uaURL := siteURL + "/api/v1/auth/universal-auth/identities/" + identityId
	data, status, err := infisicalDo(ctx, http.MethodPost, uaURL, token, map[string]any{})
	if err != nil {
		return "", err
	}
	if status >= 200 && status < 300 {
		return parseUniversalAuthClientId(data)
	}
	// Already attached (conflict): read the existing universal-auth config.
	data, status, err = infisicalDo(ctx, http.MethodGet, uaURL, token, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("infisical: attach/read universal auth failed (HTTP %d): %s", status, data)
	}
	return parseUniversalAuthClientId(data)
}

func parseUniversalAuthClientId(data []byte) (string, error) {
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

// ensureProjectMembership adds the identity to the project as no-access (its read
// scope comes from the additional privilege). An already-member conflict is fine.
func ensureProjectMembership(ctx context.Context, siteURL, token, projectId, identityId string) error {
	url := siteURL + "/api/v1/projects/" + projectId + "/memberships/identities/" + identityId
	body := map[string]any{"role": "no-access"}
	data, status, err := infisicalDo(ctx, http.MethodPost, url, token, body)
	if err != nil {
		return err
	}
	if status == http.StatusConflict || status == http.StatusBadRequest || (status >= 200 && status < 300) {
		return nil
	}
	return fmt.Errorf("infisical: add identity to project failed (HTTP %d): %s", status, data)
}

// ensureReadPrivilege grants the identity read access to secretPath within the
// project's environment. The privilege slug is stable per identity so re-runs
// adopt the existing privilege (a conflict is tolerated).
func ensureReadPrivilege(ctx context.Context, siteURL, token, projectId, identityId, envSlug, secretPath string) error {
	body := map[string]any{
		"identityId": identityId,
		"projectId":  projectId,
		"slug":       "inforge-read-scope",
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
	url := siteURL + "/api/v2/identity-project-additional-privilege"
	data, status, err := infisicalDo(ctx, http.MethodPost, url, token, body)
	if err != nil {
		return err
	}
	if status == http.StatusConflict || status == http.StatusBadRequest || (status >= 200 && status < 300) {
		return nil
	}
	return fmt.Errorf("infisical: grant read privilege failed (HTTP %d): %s", status, data)
}

// mintClientSecret creates a non-expiring, unlimited-use client secret for the
// identity's universal auth and returns the plaintext value (returned only once
// by Infisical).
func mintClientSecret(ctx context.Context, siteURL, token, identityId string) (string, error) {
	url := siteURL + "/api/v1/auth/universal-auth/identities/" + identityId + "/client-secrets"
	body := map[string]any{
		"description":  "inforge-managed",
		"ttl":          0,
		"numUsesLimit": 0,
	}
	data, status, err := infisicalDo(ctx, http.MethodPost, url, token, body)
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
