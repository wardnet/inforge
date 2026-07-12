package grafanaalert

import "fmt"

// BuiltIns returns inforge's generated alert rules for one env (ADR-0038 slice 3),
// derived from the host metrics (ADR-0031, system_*) and — when includeDatabase is
// true — the Postgres metrics (ADR-0037, postgresql_*). Thresholds are fixed; an env
// that needs different ones opts out (built_in_alerts: false) and authors custom
// alerts. Every built-in routes through the env's default_profile (Profile "").
// includeDatabase gates the Postgres alerts so a DB-less env's "metrics missing"
// alert can't fire forever.
func BuiltIns(env string, includeDatabase bool) []Alert {
	e := envMatcher(env)
	fs := `type=~"ext4|xfs"`

	// b compiles a built-in; built-in exprs are authored here so parseCondition
	// never fails — a panic here would be a programming error, surfaced by tests.
	b := func(name, expr, cond, forDur, severity, summary, noData string) Alert {
		r, err := build(name, expr, cond, forDur, severity, summary, noData, nil)
		if err != nil {
			panic(fmt.Sprintf("grafanaalert: built-in %q: %v", name, err))
		}
		return Alert{Rule: r, Severity: severity}
	}

	alerts := []Alert{
		b("Host CPU Saturated",
			fmt.Sprintf(`100 * (1 - (sum by (instance) (rate(system_cpu_time_seconds_total{state="idle", %s}[5m])) / sum by (instance) (rate(system_cpu_time_seconds_total{%s}[5m]))))`, e, e),
			"> 90", "10m", SeverityWarning,
			"CPU on {{ $labels.instance }} above 90% for 10m ({{ $value }}%).", NoDataOK),

		b("Host Memory Saturated",
			fmt.Sprintf(`100 * sum by (instance) (system_memory_usage_bytes{state="used", %s}) / sum by (instance) (system_memory_usage_bytes{%s})`, e, e),
			"> 90", "10m", SeverityWarning,
			"Memory on {{ $labels.instance }} above 90% for 10m ({{ $value }}%).", NoDataOK),

		b("Host Disk Saturated",
			fmt.Sprintf(`100 * sum by (instance, mountpoint) (system_filesystem_usage_bytes{state="used", %s, %s}) / sum by (instance, mountpoint) (system_filesystem_usage_bytes{%s, %s})`, fs, e, fs, e),
			"> 90", "5m", SeverityCritical,
			"Disk {{ $labels.mountpoint }} on {{ $labels.instance }} above 90% ({{ $value }}%).", NoDataOK),

		b("Host Disk Will Fill",
			fmt.Sprintf(`predict_linear(system_filesystem_usage_bytes{state="free", %s, %s}[6h], 86400)`, fs, e),
			"< 0", "1h", SeverityWarning,
			"Disk {{ $labels.mountpoint }} on {{ $labels.instance }} is projected to fill within 24h.", NoDataOK),

		b("Host Rebooted",
			fmt.Sprintf(`resets(system_uptime_seconds{%s}[10m])`, e),
			"> 0", "0", SeverityWarning,
			"{{ $labels.instance }} rebooted in the last 10m.", NoDataOK),

		b("Host Metrics Missing",
			fmt.Sprintf(`count(system_uptime_seconds{%s})`, e),
			"< 1", "5m", SeverityCritical,
			"No host is reporting metrics for this environment.", NoDataAlerting),
	}

	if includeDatabase {
		// Postgres series are keyed by cluster identity, not by host: one host can run
		// several clusters, so grouping by `instance` would sum backends across clusters
		// and divide by a single cluster's max_connections. Group by db_cluster_name —
		// the same label the Database dashboard groups by (ADR-0038 slice 4).
		alerts = append(alerts,
			b("Postgres Connections High",
				fmt.Sprintf(`100 * sum by (db_cluster_name) (postgresql_backends{%s}) / clamp_min(max by (db_cluster_name) (postgresql_connection_max{%s}), 1)`, e, e),
				"> 80", "10m", SeverityWarning,
				"Postgres {{ $labels.db_cluster_name }} connection usage above 80% ({{ $value }}%).", NoDataOK),

			b("Postgres High Rollback Ratio",
				fmt.Sprintf(`100 * sum by (db_cluster_name) (rate(postgresql_rollbacks_total{%s}[5m])) / clamp_min(sum by (db_cluster_name) (rate(postgresql_commits_total{%s}[5m]) + rate(postgresql_rollbacks_total{%s}[5m])), 1)`, e, e, e),
				"> 25", "15m", SeverityWarning,
				"Postgres {{ $labels.db_cluster_name }} rollback ratio above 25% ({{ $value }}%).", NoDataOK),

			b("Postgres Metrics Missing",
				fmt.Sprintf(`count(postgresql_backends{%s})`, e),
				"< 1", "5m", SeverityCritical,
				"No Postgres cluster is reporting metrics for this environment.", NoDataAlerting),
		)
	}

	return alerts
}
