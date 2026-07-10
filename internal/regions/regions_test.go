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

func TestTableSortedNames(t *testing.T) {
	tbl := Table{
		"us-east-1":    {Slug: "use1"},
		"ap-east-1":    {Slug: "ape1"},
		"eu-central-1": {Slug: "euc1"},
	}
	assert.Equal(t, []string{"ap-east-1", "eu-central-1", "us-east-1"}, tbl.SortedNames())
}

func TestTableSortedNamesEmpty(t *testing.T) {
	assert.Empty(t, Table{}.SortedNames())
}

func TestDefaultTableIsACopy(t *testing.T) {
	a := DefaultTable()
	a["us-east-1"] = AbstractRegion{Slug: "mutated"}
	b := DefaultTable()
	slug, err := b.Slug("us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "use1", slug, "DefaultTable must return a fresh table each call")
}
