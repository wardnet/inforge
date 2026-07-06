package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validDescriptor = `version: 7
service: ghost
exec: /srv/wardnet/ghost/run
user: ghost
env:
  DATABASE_URL: DATABASE_URL
  STRIPE_KEY: STRIPE_KEY
`

func TestParseDescriptorValid(t *testing.T) {
	d, err := ParseDescriptor([]byte(validDescriptor))
	require.NoError(t, err)

	assert.Equal(t, SupportedVersion, d.Version)
	assert.Equal(t, "ghost", d.Service)
	assert.Equal(t, "/srv/wardnet/ghost/run", d.Exec)
	assert.Equal(t, "ghost", d.User)
	assert.Equal(t, "DATABASE_URL", d.Env["DATABASE_URL"])
	assert.Equal(t, "STRIPE_KEY", d.Env["STRIPE_KEY"])
}

// TestParseDescriptorRejectsUnknownVersion is the fleet-skew safety: a descriptor
// from a newer major must fail the start, never be misread.
func TestParseDescriptorRejectsUnknownVersion(t *testing.T) {
	// version: 6 is the pre-ADR-0035 (Provider-block) schema — a v7 agent must
	// reject it cleanly on the version, not misread it.
	for _, v := range []string{"version: 1", "version: 0", "version: 2", "version: 6", "version: 99"} {
		doc := v + "\nservice: ghost\nexec: /x\nuser: ghost\n"
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
		"service": "version: 7\nexec: /x\nuser: ghost\n",
		"exec":    "version: 7\nservice: ghost\nuser: ghost\n",
		"user":    "version: 7\nservice: ghost\nexec: /x\n",
	}
	for missing, doc := range cases {
		_, err := ParseDescriptor([]byte(doc))
		assert.Error(t, err, "missing %s must error", missing)
	}
}

// TestParseDescriptorSecretLess: a descriptor with no env is a secret-less
// service — always valid (ADR-0035: no provider concept left to gate it).
func TestParseDescriptorSecretLess(t *testing.T) {
	doc := "version: 7\nservice: ghost\nexec: /x\nuser: ghost\n"
	d, err := ParseDescriptor([]byte(doc))
	require.NoError(t, err)
	assert.Empty(t, d.Env)
}
