package grafanadash

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomOverridesUID(t *testing.T) {
	raw := []byte(`{"uid":"authors-own","title":"My Board","panels":[]}`)
	out, err := Custom("my-board", "inforge-prd-custom-my-board", raw, false)
	require.NoError(t, err)
	m := parse(t, out)

	// The author's uid is replaced with the env-prefixed one; title/panels survive.
	assert.Equal(t, "inforge-prd-custom-my-board", m["uid"])
	assert.Equal(t, "My Board", m["title"])
	assert.NotContains(t, out, "authors-own")
}

func TestCustomUnwrapsAPIShape(t *testing.T) {
	// The "get by UID" API form wraps the model under "dashboard".
	raw := []byte(`{"meta":{"folderId":3},"dashboard":{"uid":"x","title":"Wrapped","panels":[]}}`)
	out, err := Custom("wrapped", "inforge-stg-custom-wrapped", raw, false)
	require.NoError(t, err)
	m := parse(t, out)

	assert.Equal(t, "inforge-stg-custom-wrapped", m["uid"])
	assert.Equal(t, "Wrapped", m["title"])
	// The API envelope is gone — no leftover "meta".
	_, hasMeta := m["meta"]
	assert.False(t, hasMeta)
}

func TestCustomParsesYAML(t *testing.T) {
	raw := []byte("uid: y\ntitle: YAML Board\npanels: []\n")
	out, err := Custom("yaml-board", "inforge-prd-custom-yaml-board", raw, true)
	require.NoError(t, err)
	m := parse(t, out)

	assert.Equal(t, "inforge-prd-custom-yaml-board", m["uid"])
	assert.Equal(t, "YAML Board", m["title"])
}

func TestCustomRejectsGarbage(t *testing.T) {
	_, err := Custom("bad", "u", []byte("{not json"), false)
	assert.Error(t, err)
}
