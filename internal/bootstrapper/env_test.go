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
