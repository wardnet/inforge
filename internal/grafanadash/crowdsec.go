package grafanadash

import "fmt"

// Crowdsec renders the built-in CrowdSec dashboard for one env (ADR-0043) from the agent
// + firewall-bouncer metrics scraped on edge hosts (service.name wardnet-crowdsec-metrics):
// an overview (active bans, acquisition health, edges reporting), acquisition/detection
// detail, decisions, and the firewall bouncer's drops + pull freshness. uid is the
// env-prefixed dashboard UID (grafana.DashboardUID). The CrowdSec metric names are the
// standard cs_* series; a name that turns out different on the running agent simply shows
// "No data" in its panel (harmless), unlike an alert.
func Crowdsec(env, uid string) (string, error) {
	e := envMatcher(env)
	inst := `instance=~"$instance"`
	ri := "$__rate_interval"

	vars := []map[string]any{queryVar("instance", "Edge", "cs_parser_hits_total", e)}

	panels := []map[string]any{
		row("Overview", 0),
		stat("Active Decisions", gp(0, 1, 5, 4),
			[]map[string]any{target(fmt.Sprintf("sum(cs_active_decisions{%s})", e), "__auto")},
			"none", "Currently active IP ban decisions across all edges.", nil),
		stat("Bans Added (range)", gp(5, 1, 5, 4),
			[]map[string]any{target(fmt.Sprintf("sum(increase(cs_lapi_decisions_total{%s}[$__range]))", e), "__auto")},
			"none", "New decisions recorded in the selected time range.", nil),
		stat("Acquisition Rate", gp(10, 1, 7, 4),
			[]map[string]any{target(fmt.Sprintf("sum(rate(cs_parser_hits_total{%s}[%s]))", e, ri), "__auto")},
			"cps", "Log lines parsed per second — the health signal. Zero on a busy edge means CrowdSec is blind.", nil),
		stat("Edges Reporting", gp(17, 1, 7, 4),
			[]map[string]any{target(fmt.Sprintf("count(count by (instance) (cs_parser_hits_total{%s}))", e), "__auto")},
			"none", "Edge hosts reporting CrowdSec metrics.", nil),

		row("Acquisition & Detection", 5),
		ts("Log acquisition rate by source", gp(0, 6, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (source) (rate(cs_reader_hits_total{%s}[%s]))", e, ri), "{{source}}"}),
			"cps"),
		ts("Scenario hits", gp(12, 6, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (name) (rate(cs_scenario_hits_total{%s}[%s]))", e, ri), "{{name}}"}),
			"cps"),

		row("Decisions", 14),
		ts("Decisions added by reason", gp(0, 15, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (reason) (rate(cs_lapi_decisions_total{%s}[%s]))", e, ri), "{{reason}}"}),
			"cps"),
		ts("Active decisions by origin", gp(12, 15, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (origin) (cs_active_decisions{%s})", e), "{{origin}}"}),
			"none"),

		row("Firewall Bouncer", 23),
		ts("Bouncer dropped packets/s by edge", gp(0, 24, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (instance) (rate(cs_bouncer_dropped_packets_total{%s, %s}[%s]))", e, inst, ri), "{{instance}}"}),
			"cps"),
		ts("Bouncer dropped bytes/s by edge", gp(12, 24, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (instance) (rate(cs_bouncer_dropped_bytes_total{%s, %s}[%s]))", e, inst, ri), "{{instance}}"}),
			"Bps"),
		stat("Bouncer last-pull age by edge", gp(0, 32, 24, 6),
			[]map[string]any{target(fmt.Sprintf("time() - max by (instance) (cs_bouncer_last_pull{%s, %s})", e, inst), "{{instance}}")},
			"s", "Seconds since each edge's firewall bouncer last pulled decisions. A growing value means enforcement is drifting.",
			thresholds([2]any{"green", nil}, [2]any{"orange", 300}, [2]any{"red", 900})),
	}

	return dashboard("CrowdSec Monitoring", uid, []string{"inforge", "otel", "crowdsec"}, vars, panels)
}
