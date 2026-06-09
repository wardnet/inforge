package bootstrapper

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildEnv(t *testing.T) {
	d := Descriptor{
		User: "ghost",
		Env: map[string]string{
			"DATABASE_URL": "infra/DATABASE_URL",
			"STRIPE_KEY":   "custom/STRIPE_KEY",
		},
	}
	secrets := map[string]string{
		"infra/DATABASE_URL": "postgres://secret",
		"custom/STRIPE_KEY":  "sk_live_secret",
	}

	env, err := buildEnv(d, secrets, "/home/ghost")
	require.NoError(t, err)

	assert.Contains(t, env, "PATH="+minimalPATH)
	assert.Contains(t, env, "HOME=/home/ghost")
	assert.Contains(t, env, "USER=ghost")
	assert.Contains(t, env, "LOGNAME=ghost")
	assert.Contains(t, env, "DATABASE_URL=postgres://secret")
	assert.Contains(t, env, "STRIPE_KEY=sk_live_secret")
}

// TestBuildEnvSecretLess: a secret-less service (no env mapping, nil secrets)
// gets only the minimal base env — the path run.go takes when there is no
// provider, so it never touches a fetcher.
func TestBuildEnvSecretLess(t *testing.T) {
	d := Descriptor{User: "ghost"}

	env, err := buildEnv(d, nil, "/home/ghost")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"PATH=" + minimalPATH,
		"HOME=/home/ghost",
		"USER=ghost",
		"LOGNAME=ghost",
	}, env)
}

// TestBuildEnvDeployment: the deployment context is injected as INFORGE_DEPLOYMENT_*
// for every service (here, secret-less), independent of secrets.
func TestBuildEnvDeployment(t *testing.T) {
	d := Descriptor{
		User: "bridge",
		Deployment: Deployment{
			Region:      "us-east-1",
			RegionSlug:  "use1",
			Environment: "prd",
			BaseDomain:  "wardnet.network",
			Namespace:   "prd.use1.bridge",
			FQDN:        "bridge.svc.prd.use1.wardnet.network",
		},
	}

	env, err := buildEnv(d, nil, "/home/bridge")
	require.NoError(t, err)

	assert.Contains(t, env, "INFORGE_DEPLOYMENT_REGION=us-east-1")
	assert.Contains(t, env, "INFORGE_DEPLOYMENT_REGION_SLUG=use1")
	assert.Contains(t, env, "INFORGE_DEPLOYMENT_ENVIRONMENT=prd")
	assert.Contains(t, env, "INFORGE_DEPLOYMENT_BASE_DOMAIN=wardnet.network")
	assert.Contains(t, env, "INFORGE_DEPLOYMENT_NAMESPACE=prd.use1.bridge")
	assert.Contains(t, env, "INFORGE_DEPLOYMENT_FQDN=bridge.svc.prd.use1.wardnet.network")
}

// TestBuildEnvRejectsReservedName: a secret mapped to a reserved INFORGE_* name
// must fail the start rather than emit a duplicate that collides with the injected
// deployment context.
func TestBuildEnvRejectsReservedName(t *testing.T) {
	d := Descriptor{User: "ghost", Provider: Provider{Kind: "infisical"}, Env: map[string]string{"INFORGE_DEPLOYMENT_REGION": "infra/INFORGE_DEPLOYMENT_REGION"}}

	_, err := buildEnv(d, map[string]string{"infra/INFORGE_DEPLOYMENT_REGION": "x"}, "/home/ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

// TestBuildEnvMissingSecretFails: a mapped var with no (or empty) secret must
// fail the start — never exec the service with a blank secret.
func TestBuildEnvMissingSecretFails(t *testing.T) {
	d := Descriptor{User: "ghost", Env: map[string]string{"DATABASE_URL": "infra/DATABASE_URL"}}

	_, err := buildEnv(d, map[string]string{}, "/home/ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found or empty")

	_, err = buildEnv(d, map[string]string{"infra/DATABASE_URL": ""}, "/home/ghost")
	require.Error(t, err, "empty value must also fail")
}

// TestBuildEnvDeterministicOrder: env vars are emitted in sorted order so the
// output is stable.
func TestBuildEnvDeterministicOrder(t *testing.T) {
	d := Descriptor{User: "ghost", Env: map[string]string{"ZED": "infra/ZED", "ABE": "infra/ABE"}}
	secrets := map[string]string{"infra/ZED": "z", "infra/ABE": "a"}

	env, err := buildEnv(d, secrets, "/home/ghost")
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
