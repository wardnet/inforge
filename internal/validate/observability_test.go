package validate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/types"
)

func writeNotif(t *testing.T, dir, body string) {
	t.Helper()
	root := filepath.Join(dir, "prd", "observability")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "notifications.yaml"), []byte(body), 0o644))
}

func writeAlerts(t *testing.T, dir, body string) {
	t.Helper()
	root := filepath.Join(dir, "prd", "observability")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "alerts.yaml"), []byte(body), 0o644))
}

func obsCfg(profile string) types.ObservabilityConfig {
	return types.ObservabilityConfig{GrafanaURL: "https://g", DefaultProfile: profile}
}

const goodNotif = `
contact_points:
  pd-high: { pagerduty: { key_secret: pagerduty_key } }
  pd-low:  { pagerduty: { key_secret: pagerduty_key } }
profiles:
  prod:
    critical: { contact_point: pd-high }
    warning:  { contact_point: pd-low }
`

func TestCheckObservabilityValid(t *testing.T) {
	dir := t.TempDir()
	writeNotif(t, dir, goodNotif)
	r := &reporter{}
	checkObservability(r, dir, "prd", obsCfg("prod"))
	assert.False(t, r.failed)
}

func TestCheckObservabilityDefaultProfileMissingRoute(t *testing.T) {
	dir := t.TempDir()
	// prod routes critical but not warning — built-ins need both.
	writeNotif(t, dir, `
contact_points:
  pd-high: { pagerduty: { key_secret: pagerduty_key } }
profiles:
  prod:
    critical: { contact_point: pd-high }
`)
	r := &reporter{}
	checkObservability(r, dir, "prd", obsCfg("prod"))
	assert.True(t, r.failed)
}

func TestCheckObservabilityUnknownDefaultProfile(t *testing.T) {
	dir := t.TempDir()
	writeNotif(t, dir, goodNotif)
	r := &reporter{}
	checkObservability(r, dir, "prd", obsCfg("nope"))
	assert.True(t, r.failed)
}

func TestCheckObservabilityContactPointTwoIntegrations(t *testing.T) {
	dir := t.TempDir()
	writeNotif(t, dir, `
contact_points:
  bad: { pagerduty: { key_secret: k }, email: { addresses: [a@x.io] } }
profiles:
  prod:
    critical: { contact_point: bad }
    warning:  { contact_point: bad }
`)
	r := &reporter{}
	checkObservability(r, dir, "prd", obsCfg("prod"))
	assert.True(t, r.failed)
}

func TestCheckObservabilityProfileUnknownContactPoint(t *testing.T) {
	dir := t.TempDir()
	writeNotif(t, dir, `
contact_points:
  pd-high: { pagerduty: { key_secret: k } }
profiles:
  prod:
    critical: { contact_point: ghost }
    warning:  { contact_point: pd-high }
`)
	r := &reporter{}
	checkObservability(r, dir, "prd", obsCfg("prod"))
	assert.True(t, r.failed)
}

func TestCheckObservabilityProfileRouteBothSet(t *testing.T) {
	dir := t.TempDir()
	writeNotif(t, dir, `
contact_points:
  pd-high: { pagerduty: { key_secret: k } }
profiles:
  prod:
    critical: { contact_point: pd-high, muted: true }
    warning:  { contact_point: pd-high }
`)
	r := &reporter{}
	checkObservability(r, dir, "prd", obsCfg("prod"))
	assert.True(t, r.failed)
}

func TestCheckObservabilityCustomAlertRouting(t *testing.T) {
	dir := t.TempDir()
	writeNotif(t, dir, goodNotif)
	// info is not routed by prod, and this alert (default profile) is info-severity.
	writeAlerts(t, dir, `
alerts:
  - name: quiet
    expr: sum(x)
    condition: "< 1"
    severity: info
`)
	r := &reporter{}
	checkObservability(r, dir, "prd", obsCfg("prod"))
	assert.True(t, r.failed)
}

func TestCheckObservabilityBadCustomCondition(t *testing.T) {
	dir := t.TempDir()
	writeNotif(t, dir, goodNotif)
	writeAlerts(t, dir, `
alerts:
  - name: bad
    expr: sum(x)
    condition: "way too high"
    severity: warning
`)
	r := &reporter{}
	checkObservability(r, dir, "prd", obsCfg("prod"))
	assert.True(t, r.failed)
}

func TestCheckObservabilityMutedIsValid(t *testing.T) {
	dir := t.TempDir()
	writeNotif(t, dir, `
contact_points:
  pd-high: { pagerduty: { key_secret: k } }
profiles:
  prod:
    critical: { contact_point: pd-high }
    warning:  { muted: true }
`)
	r := &reporter{}
	checkObservability(r, dir, "prd", obsCfg("prod"))
	assert.False(t, r.failed)
}
