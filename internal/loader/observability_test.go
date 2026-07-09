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

func writeObs(t *testing.T, dir, file, body string) {
	t.Helper()
	root := filepath.Join(dir, "prd", "observability")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, file), []byte(body), 0o644))
}

func TestLoadNotifications(t *testing.T) {
	dir := t.TempDir()
	writeObs(t, dir, "notifications.yaml", `
contact_points:
  oncall-high: { oncall: { url_secret: oncall_url } }
  team-email: { email: { addresses: [ops@x.io] } }
profiles:
  prod:
    critical: { contact_point: oncall-high }
    warning:  { muted: true }
`)
	got, err := LoadNotifications("prd", dir)
	require.NoError(t, err)
	require.Contains(t, got.ContactPoints, "oncall-high")
	assert.Equal(t, "oncall_url", got.ContactPoints["oncall-high"].OnCall.URLSecret)
	assert.Equal(t, "oncall-high", got.Profiles["prod"]["critical"].ContactPoint)
	assert.True(t, got.Profiles["prod"]["warning"].Muted)
}

func TestLoadNotificationsMissing(t *testing.T) {
	got, err := LoadNotifications("prd", t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, got.ContactPoints)
}

func TestLoadAlertsTrims(t *testing.T) {
	dir := t.TempDir()
	writeObs(t, dir, "alerts.yaml", `
alerts:
  - name: "  drop  "
    expr: "  sum(x)  "
    condition: "< 1"
    severity: warning
`)
	got, err := LoadAlerts("prd", dir)
	require.NoError(t, err)
	require.Len(t, got.Alerts, 1)
	assert.Equal(t, "drop", got.Alerts[0].Name)
	assert.Equal(t, "sum(x)", got.Alerts[0].Expr)
}
