package grafanadash

import "fmt"

// Crowdsec renders the built-in CrowdSec dashboard for one env (ADR-0043) from the agent
// + firewall-bouncer metrics scraped on edge hosts (service.name wardnet-crowdsec-metrics):
// an overview (active bans, acquisition health, edges reporting), acquisition/detection
// detail, decisions, and the firewall bouncer's drops + LAPI pull activity. uid is the
// env-prefixed dashboard UID (grafana.DashboardUID).
//
// Every metric name below was confirmed against a real host (crowdsec 1.7.8 +
// crowdsec-firewall-bouncer-nftables 0.0.34 on Ubuntu noble). Two naming facts are easy to
// get wrong and are load-bearing here:
//   - The AGENT exposes cs_* on :6060, but the BOUNCER exposes fw_bouncer_* (NOT cs_bouncer_*)
//     with no _total suffix, plus a bare lapi_requests_total, on :60601.
//   - There is no cs_scenario_hits_total / cs_reader_hits_total / cs_lapi_decisions_total —
//     the real series are cs_bucket_poured_total, cs_filesource_hits_total and the
//     cs_active_decisions gauge. Decisions are a GAUGE only; no add-counter exists, so no
//     panel here uses increase() over decisions.
//
// fw_bouncer_dropped_* are declared `gauge` though they are cumulative totals; rate() is
// still correct over them (it treats a decrease as a reset), which is what the drop panels use.
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
		// CrowdSec publishes no decisions-added counter, so this reports what the firewall
		// is actually enforcing (bouncer-side) next to what LAPI knows (cs_active_decisions).
		stat("IPs Banned at Firewall", gp(5, 1, 5, 4),
			[]map[string]any{target(fmt.Sprintf("sum(fw_bouncer_banned_ips{%s})", e), "__auto")},
			"none", "IP addresses currently loaded into the edge firewall's nftables ban sets.", nil),
		stat("Acquisition Rate", gp(10, 1, 7, 4),
			[]map[string]any{target(fmt.Sprintf("sum(rate(cs_parser_hits_total{%s}[%s]))", e, ri), "__auto")},
			"cps", "Log lines parsed per second — the health signal. Zero on a busy edge means CrowdSec is blind.", nil),
		stat("Edges Reporting", gp(17, 1, 7, 4),
			[]map[string]any{target(fmt.Sprintf("count(count by (instance) (cs_parser_hits_total{%s}))", e), "__auto")},
			"none", "Edge hosts reporting CrowdSec metrics.", nil),

		row("Acquisition & Detection", 5),
		ts("Log acquisition rate by source", gp(0, 6, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (source) (rate(cs_filesource_hits_total{%s}[%s]))", e, ri), "{{source}}"}),
			"cps"),
		ts("Scenario hits", gp(12, 6, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (name) (rate(cs_bucket_poured_total{%s}[%s]))", e, ri), "{{name}}"}),
			"cps"),

		row("Decisions", 14),
		ts("Active decisions by reason", gp(0, 15, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (reason) (cs_active_decisions{%s})", e), "{{reason}}"}),
			"none"),
		ts("Active decisions by origin", gp(12, 15, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (origin) (cs_active_decisions{%s})", e), "{{origin}}"}),
			"none"),

		row("Firewall Bouncer", 23),
		ts("Bouncer dropped packets/s by edge", gp(0, 24, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (instance) (rate(fw_bouncer_dropped_packets{%s, %s}[%s]))", e, inst, ri), "{{instance}}"}),
			"cps"),
		ts("Bouncer dropped bytes/s by edge", gp(12, 24, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (instance) (rate(fw_bouncer_dropped_bytes{%s, %s}[%s]))", e, inst, ri), "{{instance}}"}),
			"Bps"),
		// The bouncer exposes no last-pull timestamp; its LAPI call counter is the freshness
		// signal instead — a healthy bouncer polls /v1/decisions/stream continuously, so a
		// flat-zero rate means it has stopped pulling and enforcement is drifting.
		ts("Bouncer LAPI pull rate by edge", gp(0, 32, 24, 6),
			targets([2]string{fmt.Sprintf("sum by (instance) (rate(lapi_requests_total{%s, %s}[%s]))", e, inst, ri), "{{instance}}"}),
			"reqps"),
	}

	return dashboard("CrowdSec Monitoring", uid, []string{"inforge", "otel", "crowdsec"}, vars, panels)
}
