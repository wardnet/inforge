package grafanadash

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceRenders(t *testing.T) {
	out, err := Service("prd", "inforge-prd-service")
	require.NoError(t, err)
	m := parse(t, out)

	assert.Equal(t, "inforge-prd-service", m["uid"])
	assert.Equal(t, "Service Monitoring", m["title"])
	// Every query is env-scoped, and the RED + domain signals are present.
	assert.Contains(t, out, `deployment_environment_name=\"prd\"`)
	for _, want := range []string{
		"RED Overview", "Per Service", "Domain (wardnet-cloud)",
		"http_server_request_duration_seconds_count",
		"http_server_request_duration_seconds_bucket",
		"histogram_quantile",
		`http_response_status_code=~\"5..\"`,
		"or vector(0)", // fleet error-rate stat reads 0%, not "No data", when there are zero 5xx
		"ddns_networks_provisioned_total",
		"tunneller_active_tunnels",
		"tenants_tombstone_sweep_runs_total",
		"tenants_tombstone_sweep_deleted_total",
		`"label":"Service"`,
	} {
		assert.Contains(t, out, want)
	}
}

func TestServiceDeterministic(t *testing.T) {
	a, err := Service("prd", "u")
	require.NoError(t, err)
	b, err := Service("prd", "u")
	require.NoError(t, err)
	assert.Equal(t, a, b, "render must be byte-stable")
}
