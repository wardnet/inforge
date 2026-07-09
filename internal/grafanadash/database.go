package grafanadash

import "fmt"

// Database renders the built-in Postgres dashboard for one env from the ADR-0037
// postgresql receiver metrics: a fleet overview, a per-cluster section, and a
// per-database drill-down. The ADR-0038 label-promotion slice (slice 4) turned
// db.cluster.name and postgresql.database.name into real series labels
// (db_cluster_name / postgresql_database_name), so grouping is by cluster and
// database identity rather than the host `instance` — a Cluster variable selects
// clusters and a chained Database variable narrows to databases within them.
//
// Which label a metric carries follows the receiver's resource model: per-database
// metrics (postgresql_backends, postgresql_commits_total, postgresql_rollbacks_total,
// postgresql_db_size_bytes) carry both db_cluster_name and postgresql_database_name;
// server-level metrics (postgresql_connection_max, postgresql_bgwriter_*) carry only
// db_cluster_name. The panels group by whichever the metric actually has.
func Database(env, uid string) (string, error) {
	e := envMatcher(env)
	cl := `db_cluster_name=~"$cluster"`
	db := `postgresql_database_name=~"$database"`
	ri := "$__rate_interval"

	vars := []map[string]any{
		queryVarOn("cluster", "Cluster", "db_cluster_name", "postgresql_backends", e),
		// Chained to $cluster so the Database picker only offers databases in the
		// selected cluster(s).
		queryVarOn("database", "Database", "postgresql_database_name", "postgresql_backends", fmt.Sprintf("%s, %s", e, cl)),
	}

	panels := []map[string]any{
		row("Fleet Overview", 0),
		stat("Clusters", gp(0, 1, 4, 4),
			[]map[string]any{target(fmt.Sprintf("count(group by (db_cluster_name) (postgresql_backends{%s}))", e), "__auto")},
			"none", "Postgres clusters reporting.", nil),
		stat("Databases", gp(4, 1, 4, 4),
			[]map[string]any{target(fmt.Sprintf("count(group by (db_cluster_name, postgresql_database_name) (postgresql_backends{%s}))", e), "__auto")},
			"none", "Distinct databases across all clusters.", nil),
		stat("Connections", gp(8, 1, 4, 4),
			[]map[string]any{target(fmt.Sprintf("sum(postgresql_backends{%s})", e), "__auto")},
			"none", "Total active backends across all databases.", nil),
		gauge("Connection Utilization % (busiest)", gp(12, 1, 6, 4),
			[]map[string]any{target(fmt.Sprintf("max(sum by (db_cluster_name) (postgresql_backends{%s}) / clamp_min(max by (db_cluster_name) (postgresql_connection_max{%s}), 1)) * 100", e, e), "__auto")},
			"Busiest cluster's backends / max_connections."),
		stat("Total DB Size", gp(18, 1, 3, 4),
			[]map[string]any{target(fmt.Sprintf("sum(postgresql_db_size_bytes{%s})", e), "__auto")},
			"bytes", "Sum of all reported database sizes.", nil),
		stat("Commits/s", gp(21, 1, 3, 4),
			[]map[string]any{target(fmt.Sprintf("sum(rate(postgresql_commits_total{%s}[%s]))", e, ri), "__auto")},
			"ops", "Fleet commit rate.", nil),
		ts("Transactions/s (commits vs rollbacks)", gp(0, 5, 12, 8),
			targets(
				[2]string{fmt.Sprintf("sum(rate(postgresql_commits_total{%s}[%s]))", e, ri), "commits"},
				[2]string{fmt.Sprintf("sum(rate(postgresql_rollbacks_total{%s}[%s]))", e, ri), "rollbacks"},
			), "ops"),
		ts("Connections by cluster", gp(12, 5, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (db_cluster_name) (postgresql_backends{%s})", e), "{{db_cluster_name}}"}), "none"),

		row("Per Cluster", 13),
		ts("Database size by cluster", gp(0, 14, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (db_cluster_name) (postgresql_db_size_bytes{%s, %s})", e, cl), "{{db_cluster_name}}"}), "bytes"),
		ts("Commits/s by cluster", gp(12, 14, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (db_cluster_name) (rate(postgresql_commits_total{%s, %s}[%s]))", e, cl, ri), "{{db_cluster_name}}"}), "ops"),
		ts("Rollback ratio by cluster", gp(0, 22, 12, 8),
			targets([2]string{fmt.Sprintf("100 * sum by (db_cluster_name) (rate(postgresql_rollbacks_total{%s, %s}[%s])) / clamp_min(sum by (db_cluster_name) (rate(postgresql_commits_total{%s, %s}[%s]) + rate(postgresql_rollbacks_total{%s, %s}[%s])), 1)", e, cl, ri, e, cl, ri, e, cl, ri), "{{db_cluster_name}}"}), "percent"),
		ts("Checkpoints/s + buffers written by cluster", gp(12, 22, 12, 8),
			targets(
				[2]string{fmt.Sprintf("sum by (db_cluster_name) (rate(postgresql_bgwriter_checkpoint_count_total{%s, %s}[%s]))", e, cl, ri), "checkpoints {{db_cluster_name}}"},
				[2]string{fmt.Sprintf("sum by (db_cluster_name) (rate(postgresql_bgwriter_buffers_writes_total{%s, %s}[%s]))", e, cl, ri), "buffers {{db_cluster_name}}"},
			), "ops"),

		row("Per Database", 30),
		ts("Connections by database", gp(0, 31, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (db_cluster_name, postgresql_database_name) (postgresql_backends{%s, %s, %s})", e, cl, db), "{{db_cluster_name}} / {{postgresql_database_name}}"}), "none"),
		ts("Database size by database", gp(12, 31, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (db_cluster_name, postgresql_database_name) (postgresql_db_size_bytes{%s, %s, %s})", e, cl, db), "{{db_cluster_name}} / {{postgresql_database_name}}"}), "bytes"),
		ts("Commits/s by database", gp(0, 39, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (db_cluster_name, postgresql_database_name) (rate(postgresql_commits_total{%s, %s, %s}[%s]))", e, cl, db, ri), "{{db_cluster_name}} / {{postgresql_database_name}}"}), "ops"),
		ts("Rollback ratio by database", gp(12, 39, 12, 8),
			targets([2]string{fmt.Sprintf("100 * sum by (db_cluster_name, postgresql_database_name) (rate(postgresql_rollbacks_total{%s, %s, %s}[%s])) / clamp_min(sum by (db_cluster_name, postgresql_database_name) (rate(postgresql_commits_total{%s, %s, %s}[%s]) + rate(postgresql_rollbacks_total{%s, %s, %s}[%s])), 1)", e, cl, db, ri, e, cl, db, ri, e, cl, db, ri), "{{db_cluster_name}} / {{postgresql_database_name}}"}), "percent"),
	}

	return dashboard("Database Monitoring", uid, []string{"inforge", "otel", "postgres"}, vars, panels)
}
