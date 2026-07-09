package grafanadash

import "fmt"

// Infrastructure renders the built-in host/VM dashboard for one env from the ADR-0031
// host metrics: a fleet overview (all nodes) plus a per-node section filtered by a
// Node template variable. uid is the env-prefixed dashboard UID (grafana.DashboardUID).
func Infrastructure(env, uid string) (string, error) {
	e := envMatcher(env)
	inst := `instance=~"$instance"`
	disk := `device=~"sd.*|nvme.*|vd.*"`
	fs := `type=~"ext4|xfs"`
	ri := "$__rate_interval"

	vars := []map[string]any{queryVar("instance", "Node", "system_uptime_seconds", e)}

	panels := []map[string]any{
		row("Fleet Overview", 0),
		stat("Nodes Up", gp(0, 1, 4, 4),
			[]map[string]any{target(fmt.Sprintf("count(system_uptime_seconds{%s})", e), "__auto")},
			"none", "Hosts currently reporting host metrics.", nil),
		stat("Node Restarts (range)", gp(4, 1, 4, 4),
			[]map[string]any{target(fmt.Sprintf("sum(resets(system_uptime_seconds{%s}[$__range]))", e), "__auto")},
			"none", "Reboots across all nodes in the selected time range.",
			thresholds([2]any{"green", nil}, [2]any{"orange", 1})),
		gauge("CPU Used %", gp(8, 1, 5, 4),
			[]map[string]any{target(fmt.Sprintf(`100 * (1 - (sum(rate(system_cpu_time_seconds_total{state="idle", %s}[%s])) / sum(rate(system_cpu_time_seconds_total{%s}[%s]))))`, e, ri, e, ri), "__auto")},
			"Fleet CPU busy % (1 - idle)."),
		gauge("Memory Used %", gp(13, 1, 5, 4),
			[]map[string]any{target(fmt.Sprintf(`100 * sum(system_memory_usage_bytes{state="used", %s}) / sum(system_memory_usage_bytes{%s})`, e, e), "__auto")},
			"Fleet used / total (byte-weighted)."),
		gauge("Disk Used %", gp(18, 1, 6, 4),
			[]map[string]any{target(fmt.Sprintf(`100 * sum(system_filesystem_usage_bytes{state="used", %s, %s}) / sum(system_filesystem_usage_bytes{%s, %s})`, fs, e, fs, e), "__auto")},
			"Fleet used / total across real filesystems."),
		ts("Disk I/O throughput (fleet)", gp(0, 5, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (direction) (rate(system_disk_io_bytes_total{%s, %s}[%s]))", disk, e, ri), "{{direction}}"}),
			"Bps"),
		ts("Network I/O throughput (fleet)", gp(12, 5, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (direction) (rate(system_network_io_bytes_total{%s}[%s]))", e, ri), "{{direction}}"}),
			"Bps"),

		row("Per Node", 13),
		ts("CPU Used % by node", gp(0, 14, 12, 8),
			targets([2]string{fmt.Sprintf(`100 * (1 - (sum by (instance) (rate(system_cpu_time_seconds_total{state="idle", %s, %s}[%s])) / sum by (instance) (rate(system_cpu_time_seconds_total{%s, %s}[%s]))))`, e, inst, ri, e, inst, ri), "{{instance}}"}),
			"percent"),
		ts("Memory Used % by node", gp(12, 14, 12, 8),
			targets([2]string{fmt.Sprintf(`100 * sum by (instance) (system_memory_usage_bytes{state="used", %s, %s}) / sum by (instance) (system_memory_usage_bytes{%s, %s})`, e, inst, e, inst), "{{instance}}"}),
			"percent"),
		ts("Disk Used % by node / mount", gp(0, 22, 12, 8),
			targets([2]string{fmt.Sprintf(`100 * sum by (instance, mountpoint) (system_filesystem_usage_bytes{state="used", %s, %s, %s}) / sum by (instance, mountpoint) (system_filesystem_usage_bytes{%s, %s, %s})`, fs, e, inst, fs, e, inst), "{{instance}} {{mountpoint}}"}),
			"percent"),
		ts("Disk I/O by node", gp(12, 22, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (instance, direction) (rate(system_disk_io_bytes_total{%s, %s, %s}[%s]))", disk, e, inst, ri), "{{instance}} {{direction}}"}),
			"Bps"),
		ts("Network I/O by node", gp(0, 30, 24, 8),
			targets([2]string{fmt.Sprintf("sum by (instance, direction) (rate(system_network_io_bytes_total{%s, %s}[%s]))", e, inst, ri), "{{instance}} {{direction}}"}),
			"Bps"),
		stat("Uptime by node", gp(0, 38, 12, 8),
			[]map[string]any{target(fmt.Sprintf("system_uptime_seconds{%s, %s}", e, inst), "{{instance}}")},
			"s", "Seconds since each node last booted.", nil),
		stat("Restarts by node (range)", gp(12, 38, 12, 8),
			[]map[string]any{target(fmt.Sprintf("resets(system_uptime_seconds{%s, %s}[$__range])", e, inst), "{{instance}}")},
			"none", "Reboots per node in the selected time range.",
			thresholds([2]any{"green", nil}, [2]any{"orange", 1})),
	}

	return dashboard("Infrastructure Monitoring", uid, []string{"inforge", "otel", "host"}, vars, panels)
}
