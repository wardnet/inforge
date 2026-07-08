package loader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeDash(t *testing.T, dir, name, body string) {
	t.Helper()
	root := filepath.Join(dir, "prd", "observability", "dashboards")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o644))
}

func TestLoadCustomDashboardsMissingDir(t *testing.T) {
	got, err := LoadCustomDashboards("prd", t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLoadCustomDashboardsSortedAndTyped(t *testing.T) {
	dir := t.TempDir()
	writeDash(t, dir, "Zeta Board.json", `{"uid":"z"}`)
	writeDash(t, dir, "alpha.yaml", "uid: a\n")
	writeDash(t, dir, "README.md", "ignore me") // non-dashboard extension is skipped

	got, err := LoadCustomDashboards("prd", dir)
	require.NoError(t, err)
	require.Len(t, got, 2)

	// Sorted by slug: "alpha" < "zeta-board".
	assert.Equal(t, "alpha", got[0].Name)
	assert.True(t, got[0].IsYAML)
	assert.Equal(t, "zeta-board", got[1].Name)
	assert.False(t, got[1].IsYAML)
}

func TestLoadCustomDashboardsSlugCollision(t *testing.T) {
	dir := t.TempDir()
	writeDash(t, dir, "My Board.json", `{}`)
	writeDash(t, dir, "my-board.json", `{}`)

	_, err := LoadCustomDashboards("prd", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slug")
}
