package sizes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultTableResolve(t *testing.T) {
	tbl := DefaultTable()

	require.NoError(t, tbl.Resolve("MEDIUM"))

	err := tbl.Resolve("HUGE")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown compute size")
}

// TestPerEnvReplacement models the loader's replace-not-merge behaviour: a
// per-env table stands alone and need not contain the default size names.
func TestPerEnvReplacement(t *testing.T) {
	perEnv := Table{
		"tiny": {},
		"huge": {},
	}

	require.NoError(t, perEnv.Resolve("tiny"))

	// A default size is not present in a replacing table.
	assert.Error(t, perEnv.Resolve("SMALL"))
}
