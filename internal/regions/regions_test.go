package regions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

func TestAbstractRegionProvidersRoundTrip(t *testing.T) {
	const src = `
us-east-1:
  slug: use1
  providers:
    hetzner:
      location: ash
      network_zone: us-east
eu-central-1:
  slug: euc1
`
	var tbl Table
	require.NoError(t, yaml.Unmarshal([]byte(src), &tbl))

	use1 := tbl["us-east-1"]
	assert.Equal(t, "use1", use1.Slug)
	require.NotNil(t, use1.Providers)
	assert.Equal(t, "ash", use1.Providers["hetzner"]["location"])
	assert.Equal(t, "us-east", use1.Providers["hetzner"]["network_zone"])

	euc1 := tbl["eu-central-1"]
	assert.Equal(t, "euc1", euc1.Slug)
	assert.Nil(t, euc1.Providers)

	// round-trip: marshal back and re-parse
	out, err := yaml.Marshal(tbl)
	require.NoError(t, err)
	var tbl2 Table
	require.NoError(t, yaml.Unmarshal(out, &tbl2))
	assert.Equal(t, tbl["us-east-1"], tbl2["us-east-1"])
}
