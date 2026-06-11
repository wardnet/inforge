package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSourceRef(t *testing.T) {
	s, err := ParseSource("ref:database/bridge.connectionUrl")
	require.NoError(t, err)
	assert.Equal(t, SourceRef, s.Kind)
	assert.Equal(t, "database", s.RefType)
	assert.Equal(t, "bridge", s.RefName)
	assert.Equal(t, "connectionUrl", s.RefOutput)
}

func TestParseSourceRefExpandedComputeKey(t *testing.T) {
	s, err := ParseSource("ref:compute/bridge-01.publicIp")
	require.NoError(t, err)
	assert.Equal(t, SourceRef, s.Kind)
	assert.Equal(t, "compute", s.RefType)
	assert.Equal(t, "bridge-01", s.RefName)
	assert.Equal(t, "publicIp", s.RefOutput)
}

func TestParseSourceEnv(t *testing.T) {
	s, err := ParseSource("env:CLOUDFLARE_API_TOKEN")
	require.NoError(t, err)
	assert.Equal(t, SourceEnv, s.Kind)
	assert.Equal(t, "CLOUDFLARE_API_TOKEN", s.EnvName)
}

func TestParseSourceVault(t *testing.T) {
	s, err := ParseSource("vault:MY_KEY")
	require.NoError(t, err)
	assert.Equal(t, SourceVault, s.Kind)
	assert.Equal(t, "MY_KEY", s.VaultKey)
}

func TestParseSourceLiteral(t *testing.T) {
	// Any string that does not carry a recognised prefix is a verbatim literal.
	for _, in := range []struct{ src, want string }{
		{"info", "info"},
		{"https://api.example.com:443/v1", "https://api.example.com:443/v1"},
		{"plain text with spaces", "plain text with spaces"},
	} {
		s, err := ParseSource(in.src)
		require.NoError(t, err, in.src)
		assert.Equal(t, SourceLiteral, s.Kind, in.src)
		assert.Equal(t, in.want, s.LiteralValue, in.src)
	}
}

// TestParseSourceMalformed verifies that the two prefixes requiring a suffix
// (vault: and env:) reject an empty suffix — other strings either parse as
// SourceRef or fall through to SourceLiteral without an error.
func TestParseSourceMalformed(t *testing.T) {
	cases := []string{
		"vault:", // vault: requires a key name
		"env:",   // env: requires a variable name
	}
	for _, c := range cases {
		_, err := ParseSource(c)
		assert.Error(t, err, "expected error for %q", c)
	}
}
