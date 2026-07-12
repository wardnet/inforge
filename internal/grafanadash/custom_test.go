package grafanadash

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
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

// The YAML branch reads through the one reader. Nobody asks to resolve a dashboard, so
// Grafana's OWN ${DS_FOO} template syntax survives verbatim — that is the design, not a
// special case.
func TestCustomYAMLKeepsGrafanaTemplateSyntax(t *testing.T) {
	out, err := Custom("infra", "uid-1", []byte("title: Infra\ndatasource: ${DS_PROMETHEUS}\n"), true)
	if err != nil {
		t.Fatalf("Custom: %v", err)
	}
	if !strings.Contains(out, "${DS_PROMETHEUS}") {
		t.Errorf("a dashboard's own ${} template syntax must pass through untouched:\n%s", out)
	}
}

func TestCustomYAMLMalformed(t *testing.T) {
	_, err := Custom("infra", "uid-1", []byte("title: [unclosed\n"), true)
	if err == nil {
		t.Fatal("malformed YAML must fail")
	}
	if !strings.Contains(err.Error(), "infra") {
		t.Errorf("error must name the dashboard: %v", err)
	}
}
