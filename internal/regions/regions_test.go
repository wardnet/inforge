package regions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultTableSlug(t *testing.T) {
	tbl := DefaultTable()

	slug, err := tbl.Slug("us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "use1", slug)

	_, err = tbl.Slug("mars-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown abstract region")
}

func TestDefaultTableValidate(t *testing.T) {
	tbl := DefaultTable()
	assert.NoError(t, tbl.Validate("eu-central-1"))
	assert.Error(t, tbl.Validate("eu-central-9"))
}

func TestDefaultTableIsACopy(t *testing.T) {
	a := DefaultTable()
	a["us-east-1"] = AbstractRegion{Slug: "mutated"}
	b := DefaultTable()
	slug, err := b.Slug("us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "use1", slug, "DefaultTable must return a fresh table each call")
}
