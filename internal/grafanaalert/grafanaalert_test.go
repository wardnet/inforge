package grafanaalert

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/types"
)

func TestParseCondition(t *testing.T) {
	cases := []struct {
		in, op, val string
		ok          bool
	}{
		{"> 90", ">", "90", true},
		{"<=0.01", "<=", "0.01", true},
		{">= 5", ">=", "5", true},
		{"< 0", "<", "0", true},
		{"90", "", "", false},
		{"> abc", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		op, val, err := parseCondition(c.in)
		if c.ok {
			require.NoError(t, err, c.in)
			assert.Equal(t, c.op, op, c.in)
			assert.Equal(t, c.val, val, c.in)
		} else {
			assert.Error(t, err, c.in)
		}
	}
}

func TestBuiltInsGateDatabase(t *testing.T) {
	withDB := BuiltIns("prd", true)
	noDB := BuiltIns("prd", false)
	assert.Len(t, withDB, 9)
	assert.Len(t, noDB, 6)

	// Names present and env-scoped; the Postgres ones only with a database.
	names := func(as []Alert) map[string]Alert {
		m := map[string]Alert{}
		for _, a := range as {
			m[a.Rule.Name] = a
		}
		return m
	}
	wm := names(withDB)
	assert.Contains(t, wm, "Host CPU Saturated")
	assert.Contains(t, wm, "Host Disk Will Fill")
	assert.Contains(t, wm, "Postgres Metrics Missing")
	assert.NotContains(t, names(noDB), "Postgres Metrics Missing")

	// Every built-in stamps its severity label and env-scopes its query.
	for _, a := range withDB {
		assert.Equal(t, a.Severity, a.Rule.Labels["severity"], a.Rule.Name)
		assert.Equal(t, "C", a.Rule.Condition)
		require.Len(t, a.Rule.Data, 3)
		assert.Contains(t, a.Rule.Data[0].Model, `deployment_environment_name=\"prd\"`, a.Rule.Name)
	}

	// The "metrics missing" alerts escalate NoData → Alerting; ordinary ones do not.
	assert.Equal(t, NoDataAlerting, wm["Host Metrics Missing"].Rule.NoDataState)
	assert.Equal(t, NoDataOK, wm["Host CPU Saturated"].Rule.NoDataState)
}

// Postgres built-ins must aggregate by cluster identity, not by host: a host can run
// several clusters, and grouping by `instance` sums their backends while dividing by one
// cluster's max_connections. The grouping must match the Database dashboard's.
func TestBuiltInsPostgresGroupByCluster(t *testing.T) {
	byName := map[string]Alert{}
	for _, a := range BuiltIns("prd", true) {
		byName[a.Rule.Name] = a
	}

	conns := byName["Postgres Connections High"]
	require.NotEmpty(t, conns.Rule.Name)
	assert.Contains(t, conns.Rule.Data[0].Model, `sum by (db_cluster_name) (postgresql_backends`)
	assert.Contains(t, conns.Rule.Data[0].Model, `max by (db_cluster_name) (postgresql_connection_max`)
	assert.Contains(t, conns.Rule.Annotations["summary"], "{{ $labels.db_cluster_name }}")

	roll := byName["Postgres High Rollback Ratio"]
	require.NotEmpty(t, roll.Rule.Name)
	assert.Contains(t, roll.Rule.Data[0].Model, `sum by (db_cluster_name) (rate(postgresql_rollbacks_total`)
	assert.Contains(t, roll.Rule.Data[0].Model, `sum by (db_cluster_name) (rate(postgresql_commits_total`)
	assert.Contains(t, roll.Rule.Annotations["summary"], "{{ $labels.db_cluster_name }}")

	// No Postgres built-in may group or label by host.
	for _, name := range []string{"Postgres Connections High", "Postgres High Rollback Ratio", "Postgres Metrics Missing"} {
		a := byName[name]
		assert.NotContains(t, a.Rule.Data[0].Model, "by (instance)", name)
		assert.NotContains(t, a.Rule.Annotations["summary"], "$labels.instance", name)
	}
}

func TestBuiltInMathNode(t *testing.T) {
	a := BuiltIns("prd", false)[0] // Host CPU Saturated, "> 90"
	// The condition node is a math expression over the reduced series.
	assert.Contains(t, a.Rule.Data[2].Model, `"type":"math"`)
	assert.Contains(t, a.Rule.Data[2].Model, `$B > 90`)
	assert.Equal(t, "__expr__", a.Rule.Data[1].DatasourceUID) // reduce is a server-side expr
}

func TestBuiltInsDeterministic(t *testing.T) {
	a := BuiltIns("prd", true)
	b := BuiltIns("prd", true)
	require.Equal(t, len(a), len(b))
	for i := range a {
		assert.Equal(t, a[i].Rule.Data[0].Model, b[i].Rule.Data[0].Model)
	}
}

func TestCustom(t *testing.T) {
	specs := []types.AlertSpec{{
		Name: "signup-drop", Expr: "sum(rate(x[5m]))", Condition: "< 0.01",
		For: "15m", Severity: "warning", Profile: "billing", Summary: "stalled",
		Labels: map[string]string{"team": "billing"},
	}}
	got, err := Custom(specs)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "billing", got[0].Profile)
	assert.Equal(t, "warning", got[0].Severity)
	assert.Equal(t, "15m", got[0].Rule.For)
	assert.Equal(t, "billing", got[0].Rule.Labels["team"])
	assert.Equal(t, "stalled", got[0].Rule.Annotations["summary"])
	assert.Contains(t, got[0].Rule.Data[2].Model, `$B < 0.01`)
}

func TestCustomRejectsBadSpec(t *testing.T) {
	_, err := Custom([]types.AlertSpec{{Name: "x", Expr: "up", Condition: "> 1", Severity: "urgent"}})
	assert.Error(t, err) // invalid severity

	_, err = Custom([]types.AlertSpec{{Name: "x", Expr: "up", Condition: "nope", Severity: "warning"}})
	assert.Error(t, err) // invalid condition
}

func TestForDefaults(t *testing.T) {
	got, err := Custom([]types.AlertSpec{{Name: "x", Expr: "up", Condition: "> 1", Severity: "info"}})
	require.NoError(t, err)
	assert.Equal(t, "0", got[0].Rule.For)
	assert.Empty(t, strings.TrimSpace(got[0].Rule.Annotations["summary"]))
}
