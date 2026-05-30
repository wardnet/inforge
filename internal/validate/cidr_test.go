package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCIDR(t *testing.T) {
	_, err := parseCIDR("cidr", "10.0.0.0/16")
	require.NoError(t, err)

	_, err = parseCIDR("cidr", "not-a-cidr")
	require.Error(t, err)
}

func TestCIDRContains(t *testing.T) {
	parent, err := parseCIDR("p", "10.0.0.0/16")
	require.NoError(t, err)

	child, err := parseCIDR("c", "10.0.1.0/24")
	require.NoError(t, err)
	assert.True(t, cidrContains(parent, child), "child subnet should be within parent")

	outside, err := parseCIDR("o", "10.1.0.0/24")
	require.NoError(t, err)
	assert.False(t, cidrContains(parent, outside), "subnet in a different range is not contained")

	wider, err := parseCIDR("w", "10.0.0.0/8")
	require.NoError(t, err)
	assert.False(t, cidrContains(parent, wider), "a wider prefix is not contained")
}
