package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validDescriptor = `version: 6
service: ghost
exec: /srv/wardnet/ghost/run
user: ghost
provider:
  kind: infisical
  url: https://app.infisical.com
  project: proj-123
  environment: prod
  secret_path: /ghost
env:
  DATABASE_URL: infra/DATABASE_URL
  STRIPE_KEY: custom/STRIPE_KEY
`

func TestParseDescriptorValid(t *testing.T) {
	d, err := ParseDescriptor([]byte(validDescriptor))
	require.NoError(t, err)

	assert.Equal(t, SupportedVersion, d.Version)
	assert.Equal(t, "ghost", d.Service)
	assert.Equal(t, "/srv/wardnet/ghost/run", d.Exec)
	assert.Equal(t, "ghost", d.User)
	assert.Equal(t, "infisical", d.Provider.Kind)
	assert.Equal(t, "/ghost", d.Provider.SecretPath)
	assert.Equal(t, "infra/DATABASE_URL", d.Env["DATABASE_URL"])
	assert.Equal(t, "custom/STRIPE_KEY", d.Env["STRIPE_KEY"])
}

// TestParseDescriptorRejectsUnknownVersion is the fleet-skew safety: a descriptor
// from a newer major must fail the start, never be misread.
func TestParseDescriptorRejectsUnknownVersion(t *testing.T) {
	// version: 3 is the pre-HostID schema — a v4 agent must reject it
	// cleanly on the version, not misread it.
	for _, v := range []string{"version: 1", "version: 0", "version: 2", "version: 3", "version: 99"} {
		doc := v + "\nservice: ghost\nexec: /x\nuser: ghost\nprovider:\n  kind: infisical\n"
		_, err := ParseDescriptor([]byte(doc))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported descriptor version")
	}
}

// TestParseDescriptorRejectsUnknownField catches operator typos in a hand-placed
// descriptor rather than silently dropping the key.
func TestParseDescriptorRejectsUnknownField(t *testing.T) {
	doc := validDescriptor + "extra_typo: oops\n"
	_, err := ParseDescriptor([]byte(doc))
	require.Error(t, err)
}

func TestParseDescriptorRequiresFields(t *testing.T) {
	cases := map[string]string{
		"service": "version: 6\nexec: /x\nuser: ghost\nprovider:\n  kind: infisical\n",
		"exec":    "version: 6\nservice: ghost\nuser: ghost\nprovider:\n  kind: infisical\n",
		"user":    "version: 6\nservice: ghost\nexec: /x\nprovider:\n  kind: infisical\n",
	}
	for missing, doc := range cases {
		_, err := ParseDescriptor([]byte(doc))
		assert.Error(t, err, "missing %s must error", missing)
	}
}

// TestParseDescriptorSecretLess: a descriptor with no provider is a secret-less
// service — valid as long as it carries no env mapping.
func TestParseDescriptorSecretLess(t *testing.T) {
	doc := "version: 6\nservice: ghost\nexec: /x\nuser: ghost\n"
	d, err := ParseDescriptor([]byte(doc))
	require.NoError(t, err)
	assert.Equal(t, "", d.Provider.Kind)
	assert.Empty(t, d.Env)
}

// TestParseDescriptorRejectsEnvWithoutProvider: env with no provider is a
// producer bug — there is nothing to resolve the keys against.
func TestParseDescriptorRejectsEnvWithoutProvider(t *testing.T) {
	doc := "version: 6\nservice: ghost\nexec: /x\nuser: ghost\nenv:\n  DATABASE_URL: infra/DATABASE_URL\n"
	_, err := ParseDescriptor([]byte(doc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider.kind is empty")
}
