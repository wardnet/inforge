package grafanadash

import "fmt"

// Database renders the built-in Postgres dashboard for one env from the ADR-0037
// postgresql receiver metrics: a cluster overview plus a per-cluster section filtered
// by a Cluster template variable. Grouping is by `instance` (one cluster per host
// today); the ADR-0038 label-promotion slice adds db.cluster.name / database drill-down.
func Database(env, uid string) (string, error) {
	e := envMatcher(env)
	inst := `instance=~"$instance"`
	ri := "$__rate_interval"

	vars := []map[string]any{queryVar("instance", "Cluster", "postgresql_backends", e)}

	panels := []map[string]any{
		row("Cluster Overview", 0),
		stat("Clusters", gp(0, 1, 4, 4),
			[]map[string]any{target(fmt.Sprintf("count(group by (instance) (postgresql_backends{%s}))", e), "__auto")},
			"none", "Postgres cluster hosts reporting.", nil),
		stat("Connections", gp(4, 1, 4, 4),
			[]map[string]any{target(fmt.Sprintf("sum(postgresql_backends{%s})", e), "__auto")},
			"none", "Total active backends across all clusters.", nil),
		gauge("Connection Utilization % (busiest)", gp(8, 1, 6, 4),
			[]map[string]any{target(fmt.Sprintf("max(sum by (instance) (postgresql_backends{%s}) / clamp_min(max by (instance) (postgresql_connection_max{%s}), 1)) * 100", e, e), "__auto")},
			"Busiest cluster's backends / max_connections."),
		stat("Total DB Size", gp(14, 1, 5, 4),
			[]map[string]any{target(fmt.Sprintf("sum(postgresql_db_size_bytes{%s})", e), "__auto")},
			"bytes", "Sum of all reported database sizes.", nil),
		stat("Commits/s", gp(19, 1, 5, 4),
			[]map[string]any{target(fmt.Sprintf("sum(rate(postgresql_commits_total{%s}[%s]))", e, ri), "__auto")},
			"ops", "Fleet commit rate.", nil),
		ts("Transactions/s (commits vs rollbacks)", gp(0, 5, 12, 8),
			targets(
				[2]string{fmt.Sprintf("sum(rate(postgresql_commits_total{%s}[%s]))", e, ri), "commits"},
				[2]string{fmt.Sprintf("sum(rate(postgresql_rollbacks_total{%s}[%s]))", e, ri), "rollbacks"},
			), "ops"),
		ts("Connections by cluster", gp(12, 5, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (instance) (postgresql_backends{%s})", e), "{{instance}}"}), "none"),

		row("Per Cluster", 13),
		ts("Database size by cluster", gp(0, 14, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (instance) (postgresql_db_size_bytes{%s, %s})", e, inst), "{{instance}}"}), "bytes"),
		ts("Commits/s by cluster", gp(12, 14, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (instance) (rate(postgresql_commits_total{%s, %s}[%s]))", e, inst, ri), "{{instance}}"}), "ops"),
		ts("Rollback ratio by cluster", gp(0, 22, 12, 8),
			targets([2]string{fmt.Sprintf("100 * sum by (instance) (rate(postgresql_rollbacks_total{%s, %s}[%s])) / clamp_min(sum by (instance) (rate(postgresql_commits_total{%s, %s}[%s]) + rate(postgresql_rollbacks_total{%s, %s}[%s])), 1)", e, inst, ri, e, inst, ri, e, inst, ri), "{{instance}}"}), "percent"),
		ts("Checkpoints/s + buffers written by cluster", gp(12, 22, 12, 8),
			targets(
				[2]string{fmt.Sprintf("sum by (instance) (rate(postgresql_bgwriter_checkpoint_count_total{%s, %s}[%s]))", e, inst, ri), "checkpoints {{instance}}"},
				[2]string{fmt.Sprintf("sum by (instance) (rate(postgresql_bgwriter_buffers_writes_total{%s, %s}[%s]))", e, inst, ri), "buffers {{instance}}"},
			), "ops"),
	}

	return dashboard("Database Monitoring", uid, []string{"inforge", "otel", "postgres"}, vars, panels)
}
