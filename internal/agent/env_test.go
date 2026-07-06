package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildEnv(t *testing.T) {
	d := Descriptor{
		Service: "ghost",
		User:    "ghost",
		Env: map[string]string{
			"DATABASE_URL": "infra/DATABASE_URL",
			"STRIPE_KEY":   "custom/STRIPE_KEY",
		},
	}
	secrets := map[string]string{
		"infra/DATABASE_URL": "postgres://secret",
		"custom/STRIPE_KEY":  "sk_live_secret",
	}

	env, err := buildEnv(d, secrets, "/home/ghost", "abc123")
	require.NoError(t, err)

	assert.Contains(t, env, "PATH="+minimalPATH)
	assert.Contains(t, env, "HOME=/home/ghost")
	assert.Contains(t, env, "USER=ghost")
	assert.Contains(t, env, "LOGNAME=ghost")
	assert.Contains(t, env, "DATABASE_URL=postgres://secret")
	assert.Contains(t, env, "STRIPE_KEY=sk_live_secret")
	// service.namespace = service name; service.instance.id = the per-restart id.
	assert.Contains(t, env, "INFORGE_SERVICE_NAMESPACE=ghost")
	assert.Contains(t, env, "INFORGE_INSTANCE_ID=abc123")
}

// TestBuildEnvSecretLess: a secret-less service (no env mapping, nil secrets)
// gets the minimal base env plus the always-present observability identity
// (service.namespace) — the path run.go takes when there is no provider, so it
// never touches a fetcher. An empty instanceID omits INFORGE_INSTANCE_ID.
func TestBuildEnvSecretLess(t *testing.T) {
	d := Descriptor{Service: "ghost", User: "ghost"}

	env, err := buildEnv(d, nil, "/home/ghost", "")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"PATH=" + minimalPATH,
		"HOME=/home/ghost",
		"USER=ghost",
		"LOGNAME=ghost",
		"INFORGE_SERVICE_NAMESPACE=ghost",
	}, env)
}

// TestBuildEnvDeployment: the deployment context is injected as INFORGE_* for
// every service (here, secret-less), independent of secrets.
func TestBuildEnvDeployment(t *testing.T) {
	d := Descriptor{
		Service: "bridge",
		User:    "bridge",
		Deployment: Deployment{
			Region:           "us-east-1",
			RegionSlug:       "use1",
			Environment:      "prd",
			BaseDomain:       "wardnet.network",
			FQDN:             "bridge.svc.prd.use1.wardnet.network",
			HostID:           "wardnet-prd-use1-vm-bridge-01",
			CloudProvider:    "hetzner",
			CloudRegion:      "us-east",
			AvailabilityZone: "ash",
			MachineType:      "cx23",
		},
	}

	env, err := buildEnv(d, nil, "/home/bridge", "inst-9")
	require.NoError(t, err)

	assert.Contains(t, env, "INFORGE_CLOUD_PROVIDER=hetzner")
	assert.Contains(t, env, "INFORGE_CLOUD_REGION=us-east")
	assert.Contains(t, env, "INFORGE_CLOUD_AVAILABILITY_ZONE=ash")
	assert.Contains(t, env, "INFORGE_HOST_TYPE=cx23")
	assert.Contains(t, env, "INFORGE_DEPLOYMENT_REGION=us-east-1")
	assert.Contains(t, env, "INFORGE_DEPLOYMENT_REGION_SLUG=use1")
	assert.Contains(t, env, "INFORGE_DEPLOYMENT_ENV=prd")
	assert.Contains(t, env, "INFORGE_DEPLOYMENT_BASE_DOMAIN=wardnet.network")
	assert.Contains(t, env, "INFORGE_DEPLOYMENT_FQDN=bridge.svc.prd.use1.wardnet.network")
	assert.Contains(t, env, "INFORGE_HOST_ID=wardnet-prd-use1-vm-bridge-01")
	assert.Contains(t, env, "INFORGE_SERVICE_NAMESPACE=bridge")
	assert.Contains(t, env, "INFORGE_INSTANCE_ID=inst-9")
	// The renamed/removed names must NOT appear.
	assert.NotContains(t, env, "INFORGE_DEPLOYMENT_ENVIRONMENT=prd")
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "INFORGE_DEPLOYMENT_NAMESPACE="),
			"INFORGE_DEPLOYMENT_NAMESPACE was dropped in favour of INFORGE_SERVICE_NAMESPACE")
	}
}

// TestBuildEnvOmitsEmptyCloudAttrs: a deployment whose provider did not supply the
// cloud/host identity (the fields are empty) emits no INFORGE_CLOUD_*/INFORGE_HOST_TYPE
// vars at all — they are omitted, not emitted blank (the always-present deployment
// block still appears).
func TestBuildEnvOmitsEmptyCloudAttrs(t *testing.T) {
	d := Descriptor{
		Service:    "bridge",
		User:       "bridge",
		Deployment: Deployment{Region: "us-east-1", HostID: "wardnet-prd-use1-vm-bridge-01"},
	}

	env, err := buildEnv(d, nil, "/home/bridge", "inst-9")
	require.NoError(t, err)

	assert.Contains(t, env, "INFORGE_HOST_ID=wardnet-prd-use1-vm-bridge-01")
	for _, e := range env {
		for _, name := range []string{"INFORGE_CLOUD_PROVIDER", "INFORGE_CLOUD_REGION", "INFORGE_CLOUD_AVAILABILITY_ZONE", "INFORGE_HOST_TYPE"} {
			assert.False(t, strings.HasPrefix(e, name+"="), "%s must be omitted when empty", name)
		}
	}
}

// TestBuildEnvMesh: a mesh member's endpoint contract (ADR-0032) is injected as
// INFORGE_MESH_URL/SCOPE always, and INFORGE_MESH_PORT only when it exposes an
// inbound port; an egress-only member (Port == 0) omits INFORGE_MESH_PORT.
func TestBuildEnvMesh(t *testing.T) {
	d := Descriptor{
		Service: "ddns",
		User:    "ddns",
		Mesh:    &Mesh{URL: "http://127.0.0.1:9500", Port: 8080, Scope: "us-east-1"},
	}
	env, err := buildEnv(d, nil, "/home/ddns", "i")
	require.NoError(t, err)
	assert.Contains(t, env, "INFORGE_MESH_URL=http://127.0.0.1:9500")
	assert.Contains(t, env, "INFORGE_MESH_SCOPE=us-east-1")
	assert.Contains(t, env, "INFORGE_MESH_PORT=8080")

	// Egress-only member (no inbound mesh.port) omits INFORGE_MESH_PORT.
	d.Mesh = &Mesh{URL: "http://127.0.0.1:9501", Scope: "global"}
	env, err = buildEnv(d, nil, "/home/ddns", "i")
	require.NoError(t, err)
	assert.Contains(t, env, "INFORGE_MESH_URL=http://127.0.0.1:9501")
	assert.Contains(t, env, "INFORGE_MESH_SCOPE=global")
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "INFORGE_MESH_PORT="), "egress-only member must omit INFORGE_MESH_PORT")
	}

	// A non-mesh service emits no INFORGE_MESH_* at all.
	d.Mesh = nil
	env, err = buildEnv(d, nil, "/home/ddns", "i")
	require.NoError(t, err)
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "INFORGE_MESH_"), "non-mesh service must emit no INFORGE_MESH_*")
	}
}

// TestBuildEnvRejectsReservedName: a secret mapped to a reserved INFORGE_* name
// must fail the start rather than emit a duplicate that collides with the injected
// deployment context.
func TestBuildEnvRejectsReservedName(t *testing.T) {
	d := Descriptor{User: "ghost", Env: map[string]string{"INFORGE_DEPLOYMENT_REGION": "infra/INFORGE_DEPLOYMENT_REGION"}}

	_, err := buildEnv(d, map[string]string{"infra/INFORGE_DEPLOYMENT_REGION": "x"}, "/home/ghost", "i")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

// TestBuildEnvMissingSecretFails: a mapped var with no (or empty) secret must
// fail the start — never exec the service with a blank secret.
func TestBuildEnvMissingSecretFails(t *testing.T) {
	d := Descriptor{User: "ghost", Env: map[string]string{"DATABASE_URL": "infra/DATABASE_URL"}}

	_, err := buildEnv(d, map[string]string{}, "/home/ghost", "i")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found or empty")

	_, err = buildEnv(d, map[string]string{"infra/DATABASE_URL": ""}, "/home/ghost", "i")
	require.Error(t, err, "empty value must also fail")
}

// TestBuildEnvDeterministicOrder: env vars are emitted in sorted order so the
// output is stable.
func TestBuildEnvDeterministicOrder(t *testing.T) {
	d := Descriptor{User: "ghost", Env: map[string]string{"ZED": "infra/ZED", "ABE": "infra/ABE"}}
	secrets := map[string]string{"infra/ZED": "z", "infra/ABE": "a"}

	env, err := buildEnv(d, secrets, "/home/ghost", "i")
	require.NoError(t, err)
	// The four base vars come first, then ABE before ZED.
	abe, zed := -1, -1
	for i, e := range env {
		switch {
		case strings.HasPrefix(e, "ABE="):
			abe = i
		case strings.HasPrefix(e, "ZED="):
			zed = i
		}
	}
	require.NotEqual(t, -1, abe)
	require.NotEqual(t, -1, zed)
	assert.Less(t, abe, zed)
}
