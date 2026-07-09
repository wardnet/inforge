package grafanadash

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parse(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m), "dashboard must be valid JSON")
	return m
}

func TestInfrastructureRenders(t *testing.T) {
	out, err := Infrastructure("prd", "inforge-prd-infrastructure")
	require.NoError(t, err)
	m := parse(t, out)

	assert.Equal(t, "inforge-prd-infrastructure", m["uid"])
	assert.Equal(t, "Infrastructure Monitoring", m["title"])
	// Every query is scoped to this env, and the fleet/per-node signals are present.
	assert.Contains(t, out, `deployment_environment_name=\"prd\"`)
	for _, want := range []string{"Fleet Overview", "Per Node", "Node Restarts (range)", "Restarts by node (range)", "system_uptime_seconds", `instance=~\"$instance\"`} {
		assert.Contains(t, out, want)
	}
	// The Node template variable exists.
	assert.Contains(t, out, `"label":"Node"`)
}

func TestDatabaseRenders(t *testing.T) {
	out, err := Database("stg", "inforge-stg-database")
	require.NoError(t, err)
	m := parse(t, out)

	assert.Equal(t, "inforge-stg-database", m["uid"])
	assert.Equal(t, "Database Monitoring", m["title"])
	assert.Contains(t, out, `deployment_environment_name=\"stg\"`)
	for _, want := range []string{"Fleet Overview", "Per Cluster", "Per Database", "postgresql_backends", "postgresql_commits_total", `"label":"Cluster"`} {
		assert.Contains(t, out, want)
	}
	// The drill-down groups by the slice-4 promoted labels, not the host instance,
	// and offers a Database variable chained to the selected cluster.
	for _, want := range []string{"db_cluster_name", "postgresql_database_name", `"label":"Database"`, `db_cluster_name=~\"$cluster\"`} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, `instance=~\"$instance\"`)
}

func TestEnvIsolation(t *testing.T) {
	prd, err := Infrastructure("prd", "u")
	require.NoError(t, err)
	stg, err := Infrastructure("stg", "u")
	require.NoError(t, err)
	// Env is baked into the queries, so two envs never share a query body.
	assert.Contains(t, prd, `deployment_environment_name=\"prd\"`)
	assert.NotContains(t, prd, `deployment_environment_name=\"stg\"`)
	assert.Contains(t, stg, `deployment_environment_name=\"stg\"`)
}

func TestDeterministic(t *testing.T) {
	a, err := Infrastructure("prd", "u")
	require.NoError(t, err)
	b, err := Infrastructure("prd", "u")
	require.NoError(t, err)
	assert.Equal(t, a, b, "render must be byte-stable")
	assert.True(t, strings.HasPrefix(a, "{"))
}
