//go:build integration

// This file runs the Infisical client against a real, self-hosted Infisical
// instance — the only test that exercises the actual request/response contract
// (the unit tests use httptest mocks written from our own assumptions, which is
// how the v1/v2 and missing-`type` bugs slipped through). CI (ubuntu, docker
// compose) runs it as:
//
//	go test -tags=integration ./providers/infisical/internal/client/...
//
// Locally with podman, point compose at the podman socket:
//
//	INFISICAL_COMPOSE=docker-compose \
//	DOCKER_HOST="unix://$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}')" \
//	go test -tags=integration ./providers/infisical/internal/client/...
//
// By default TestMain brings up testdata/docker-compose.yml on a fresh volume,
// waits for readiness, runs the tests, and tears it down. The compose command is
// configurable via INFISICAL_COMPOSE (default "docker compose"; "docker-compose"
// for podman). Set INFISICAL_TEST_URL to point at an already-running instance and
// skip lifecycle management entirely.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const composeProject = "inforge-infisical-it"

var testBaseURL string

func TestMain(m *testing.M) {
	base := os.Getenv("INFISICAL_TEST_URL")
	managed := base == ""
	if managed {
		base = "http://localhost:8080"
		if err := composeUp(); err != nil {
			fmt.Fprintln(os.Stderr, "infisical compose up failed:", err)
			os.Exit(1)
		}
	}
	if err := waitReady(base, 90*time.Second); err != nil {
		if managed {
			composeDown()
		}
		fmt.Fprintln(os.Stderr, "infisical not ready:", err)
		os.Exit(1)
	}
	testBaseURL = base

	code := m.Run()
	if managed {
		composeDown()
	}
	os.Exit(code)
}

func composeCmd(extra ...string) *exec.Cmd {
	cmdline := os.Getenv("INFISICAL_COMPOSE")
	if cmdline == "" {
		cmdline = "docker compose"
	}
	parts := strings.Fields(cmdline)
	args := append(parts[1:], "-p", composeProject, "-f", "testdata/docker-compose.yml")
	args = append(args, extra...)
	cmd := exec.Command(parts[0], args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd
}

// composeUp tears down any prior instance (so the bootstrap below always runs on
// a fresh database) and brings the stack back up.
func composeUp() error {
	_ = composeCmd("down", "-v").Run()
	return composeCmd("up", "-d").Run()
}

func composeDown() { _ = composeCmd("down", "-v").Run() }

func waitReady(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/status")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("instance at %s not ready within %s", base, timeout)
}

// bootstrapInstance performs the one-time instance bootstrap, returning an
// org-admin machine-identity access token and the organization id. It only
// succeeds on a fresh instance (TestMain guarantees one via compose down -v).
func bootstrapInstance(t *testing.T) (token, orgID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"email":        "admin@example.com",
		"password":     "Test-Password-123",
		"organization": "wardnet-it",
	})
	resp, err := http.Post(testBaseURL+"/api/v1/admin/bootstrap", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "bootstrap must run on a fresh instance")

	var out struct {
		Organization struct {
			ID string `json:"id"`
		} `json:"organization"`
		Identity struct {
			Credentials struct {
				Token string `json:"token"`
			} `json:"credentials"`
		} `json:"identity"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotEmpty(t, out.Identity.Credentials.Token)
	require.NotEmpty(t, out.Organization.ID)
	return out.Identity.Credentials.Token, out.Organization.ID
}

// TestProvisionIdentityChainLive drives the whole adopt-or-create chain against a
// real Infisical, including the one-shot MintClientSecret that unit tests cannot
// meaningfully cover, and proves the minted credentials actually authenticate.
func TestProvisionIdentityChainLive(t *testing.T) {
	ctx := context.Background()
	token, orgID := bootstrapInstance(t)

	admin := New(testBaseURL)
	admin.SetToken(token)

	// The bootstrap token has no organizationId claim (self-hosted, like Cloud),
	// so org resolution from the token must fail — this is exactly why the
	// explicit OrganizationID knob exists and must be supplied below.
	_, err := admin.resolveOrgID("")
	assert.Error(t, err, "self-hosted UA token carries no organizationId claim")

	wsID, err := admin.AdoptOrCreateWorkspace(ctx, "chain-test")
	require.NoError(t, err)
	require.NotEmpty(t, wsID)

	// Write a secret under the path the scoped identity will be granted read on.
	require.NoError(t, admin.WriteSecrets(ctx, wsID, "dev", "/svc", map[string]string{
		"API_KEY": "s3cr3t-value",
	}))

	params := IdentityParams{
		Name:           "wardnet-it-identity",
		WorkspaceID:    wsID,
		EnvSlug:        "dev",
		SecretPath:     "/svc",
		OrganizationID: orgID,
	}
	creds, err := admin.ProvisionIdentity(ctx, params)
	require.NoError(t, err, "full chain: adopt -> universal-auth -> membership -> read-privilege -> mint")
	require.NotEmpty(t, creds.IdentityID)
	require.NotEmpty(t, creds.AuthClientID)
	require.NotEmpty(t, creds.AuthClientSecret)

	// End-to-end proof that the minted universal-auth credentials work: a fresh
	// client logs in as the provisioned identity. This validates MintClientSecret
	// and the universal-auth attach against the real server.
	svc := New(testBaseURL)
	require.NoError(t, svc.Authenticate(ctx, creds.AuthClientID, creds.AuthClientSecret),
		"the minted client credentials must authenticate")
	require.NotEmpty(t, svc.Token())

	// Idempotency: a second provision adopts the same identity rather than
	// creating a duplicate.
	creds2, err := admin.ProvisionIdentity(ctx, params)
	require.NoError(t, err)
	assert.Equal(t, creds.IdentityID, creds2.IdentityID, "re-provision must adopt the existing identity")
}
