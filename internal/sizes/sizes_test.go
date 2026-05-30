package sizes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultTableResolve(t *testing.T) {
	tbl := DefaultTable()

	s, err := tbl.Resolve("MEDIUM")
	require.NoError(t, err)
	assert.Equal(t, Size{CPUs: 4, Memory: 8}, s)

	_, err = tbl.Resolve("HUGE")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown compute size")
}

// TestPerEnvReplacement models the loader's replace-not-merge behaviour: a
// per-env table stands alone and need not contain the default size names.
func TestPerEnvReplacement(t *testing.T) {
	perEnv := Table{
		"tiny": {CPUs: 1, Memory: 1},
		"huge": {CPUs: 16, Memory: 64},
	}

	s, err := perEnv.Resolve("tiny")
	require.NoError(t, err)
	assert.Equal(t, Size{CPUs: 1, Memory: 1}, s)

	// A default size is not present in a replacing table.
	_, err = perEnv.Resolve("SMALL")
	assert.Error(t, err)
}
