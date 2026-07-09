package grafanadash

import "fmt"

// Service renders the built-in application dashboard for one env from wardnet-cloud's
// RED metrics (`http_server_request_duration_seconds_*`, ADR-0038) plus the domain
// counters/gauges inforge's telemetry contract carries: a RED fleet overview, a
// per-service section filtered by a Service template variable, and a domain row for the
// wardnet-cloud application metrics (DDNS provisioning, active tunnels, tenant tombstone
// sweeps). Grouping is by `service_name` (the OTLP service.name Grafana Cloud promotes to
// a series label, alongside `deployment_environment_name` and `region`); the histogram's
// own datapoint labels (`http_route`, `http_response_status_code`) drive the drill-downs.
func Service(env, uid string) (string, error) {
	e := envMatcher(env)
	svc := `service_name=~"$service_name"`
	ri := "$__rate_interval"

	// The RED histogram, its 5xx slice, and the total-rate denominator — reused across
	// panels so a query only differs by its grouping/selector.
	count := "http_server_request_duration_seconds_count"
	bucket := "http_server_request_duration_seconds_bucket"
	errSel := `http_response_status_code=~"5.."`

	// quantile builds a histogram_quantile expression over the bucket rate, grouped by
	// `le` plus any extra labels, under the given selectors.
	quantile := func(q float64, by, sel string) string {
		grp := "le"
		if by != "" {
			grp = "le, " + by
		}
		return fmt.Sprintf("histogram_quantile(%g, sum by (%s) (rate(%s{%s}[%s])))", q, grp, bucket, sel, ri)
	}

	vars := []map[string]any{queryVar("service_name", "Service", count, e)}

	errThresholds := thresholds([2]any{"green", nil}, [2]any{"yellow", 1}, [2]any{"red", 5})

	panels := []map[string]any{
		row("RED Overview", 0),
		stat("Request Rate", gp(0, 1, 6, 4),
			[]map[string]any{target(fmt.Sprintf("sum(rate(%s{%s}[%s]))", count, e, ri), "__auto")},
			"reqps", "Total inbound HTTP request rate across all services.", nil),
		stat("Error Rate % (5xx)", gp(6, 1, 6, 4),
			[]map[string]any{target(fmt.Sprintf("100 * sum(rate(%s{%s, %s}[%s]) or vector(0)) / clamp_min(sum(rate(%s{%s}[%s])), 1)", count, e, errSel, ri, count, e, ri), "__auto")},
			"percent", "Fleet share of responses with a 5xx status.", errThresholds),
		stat("Latency p95", gp(12, 1, 6, 4),
			[]map[string]any{target(quantile(0.95, "", e), "__auto")},
			"s", "95th-percentile request duration across the fleet.", nil),
		stat("Latency p99", gp(18, 1, 6, 4),
			[]map[string]any{target(quantile(0.99, "", e), "__auto")},
			"s", "99th-percentile request duration across the fleet.", nil),

		ts("Request rate by service", gp(0, 5, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (service_name) (rate(%s{%s}[%s]))", count, e, ri), "{{service_name}}"}),
			"reqps"),
		ts("Error rate % by service", gp(12, 5, 12, 8),
			targets([2]string{fmt.Sprintf("100 * sum by (service_name) (rate(%s{%s, %s}[%s])) / clamp_min(sum by (service_name) (rate(%s{%s}[%s])), 1)", count, e, errSel, ri, count, e, ri), "{{service_name}}"}),
			"percent"),
		ts("Latency percentiles (fleet)", gp(0, 13, 24, 8),
			targets(
				[2]string{quantile(0.5, "", e), "p50"},
				[2]string{quantile(0.95, "", e), "p95"},
				[2]string{quantile(0.99, "", e), "p99"},
			), "s"),

		row("Per Service", 21),
		ts("Request rate by status code", gp(0, 22, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (http_response_status_code) (rate(%s{%s, %s}[%s]))", count, e, svc, ri), "{{http_response_status_code}}"}),
			"reqps"),
		ts("Latency percentiles (selected)", gp(12, 22, 12, 8),
			targets(
				[2]string{quantile(0.5, "", e+", "+svc), "p50"},
				[2]string{quantile(0.95, "", e+", "+svc), "p95"},
				[2]string{quantile(0.99, "", e+", "+svc), "p99"},
			), "s"),
		ts("Request rate by route", gp(0, 30, 12, 8),
			targets([2]string{fmt.Sprintf("sum by (http_route) (rate(%s{%s, %s}[%s]))", count, e, svc, ri), "{{http_route}}"}),
			"reqps"),
		ts("p95 latency by route", gp(12, 30, 12, 8),
			targets([2]string{quantile(0.95, "http_route", e+", "+svc), "{{http_route}}"}),
			"s"),

		row("Domain (wardnet-cloud)", 38),
		ts("DDNS networks provisioned/s", gp(0, 39, 8, 8),
			targets([2]string{fmt.Sprintf("sum by (region) (rate(ddns_networks_provisioned_total{%s}[%s]))", e, ri), "{{region}}"}),
			"ops"),
		ts("Tunneller active tunnels", gp(8, 39, 8, 8),
			targets([2]string{fmt.Sprintf("sum by (region) (tunneller_active_tunnels{%s})", e), "{{region}}"}),
			"none"),
		ts("Tenant tombstone sweeps/s", gp(16, 39, 8, 8),
			targets(
				[2]string{fmt.Sprintf("sum(rate(tenants_tombstone_sweep_runs_total{%s}[%s]))", e, ri), "runs"},
				[2]string{fmt.Sprintf("sum(rate(tenants_tombstone_sweep_deleted_total{%s}[%s]))", e, ri), "deleted"},
			), "ops"),
	}

	return dashboard("Service Monitoring", uid, []string{"inforge", "otel", "service"}, vars, panels)
}
