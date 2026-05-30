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

func TestParseSourceGHA(t *testing.T) {
	s, err := ParseSource("gha:CLOUDFLARE_API_TOKEN")
	require.NoError(t, err)
	assert.Equal(t, SourceGHA, s.Kind)
	assert.Equal(t, "CLOUDFLARE_API_TOKEN", s.GHAName)
}

func TestParseSourceMalformed(t *testing.T) {
	cases := []string{
		"nonsense",
		"ref:storage/bridge.url", // unknown ref type
		"ref:database/bridge",    // missing output
		"gha:lowercase",          // gha names must be upper snake
		"gha:1LEADINGDIGIT",      // must start with letter/underscore
		"",                       // empty
		"ref:compute/bridge-01.", // empty output
	}
	for _, c := range cases {
		_, err := ParseSource(c)
		assert.Error(t, err, "expected error for %q", c)
	}
}
