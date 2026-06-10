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
	s, err := ParseSource("${CLOUDFLARE_API_TOKEN}")
	require.NoError(t, err)
	assert.Equal(t, SourceEnv, s.Kind)
	assert.Equal(t, "CLOUDFLARE_API_TOKEN", s.EnvName)
}

func TestParseSourceStatic(t *testing.T) {
	// Both keywords parse to a literal, verbatim (special characters preserved).
	for _, in := range []struct{ src, want string }{
		{"static:info", "info"},
		{"value:info", "info"},
		{"value:https://api.example.com:443/v1", "https://api.example.com:443/v1"},
		{"static:ref:database/x.y", "ref:database/x.y"}, // a literal that looks like a ref
	} {
		s, err := ParseSource(in.src)
		require.NoError(t, err, in.src)
		assert.Equal(t, SourceStatic, s.Kind, in.src)
		assert.Equal(t, in.want, s.StaticValue, in.src)
	}
}

func TestParseSourceMalformed(t *testing.T) {
	cases := []string{
		"nonsense",
		"ref:storage/bridge.url", // unknown ref type
		"ref:database/bridge",    // missing output
		"${lowercase}",           // env names must be upper snake
		"${1LEADINGDIGIT}",       // must start with letter/underscore
		"$NOBRACES",              // must be wrapped in ${...}
		"gha:OLD_FORM",           // the retired gha: form is no longer valid
		"",                       // empty
		"ref:compute/bridge-01.", // empty output
		"static:",                // empty static value
		"value:",                 // empty value alias
	}
	for _, c := range cases {
		_, err := ParseSource(c)
		assert.Error(t, err, "expected error for %q", c)
	}
}
