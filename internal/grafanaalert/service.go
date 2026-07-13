package grafanaalert

import (
	"fmt"
	"sort"
)

// ServiceScope is one service's alert eligibility, derived from its manifest — never
// authored. inforge already knows everything here, so a second source of truth (a
// `telemetry:` block on the spec) could only drift from reality: an author who set it wrong
// would get either dead alert rules or, worse, a silently unmonitored service.
type ServiceScope struct {
	// Name is the manifest name (`ddns`), used in alert titles and summaries.
	Name string
	// Metric is the OTLP `service.name` the process registers, which Grafana Cloud promotes
	// to the `service_name` series label. See naming.TelemetryServiceName.
	Metric string
	// HTTP reports whether the service serves HTTP, and so emits the RED histogram. A pure
	// worker does not, and must never be alerted on signals it cannot produce.
	HTTP bool
	// Mesh reports whether the service is a mesh member (`pki:`), and so makes outbound mesh
	// calls that the mesh-failure alert can see.
	Mesh bool
	// Database reports whether the service holds a database grant, and so has a connection
	// pool that can saturate.
	Database bool
	// Regions are the region slugs a REGIONAL service deploys to (one instance each). Empty
	// for a global service, which has a single instance and no region to distinguish.
	//
	// Liveness is per (service, region) precisely because of this: a regional service dying
	// in one region leaves the other region's series alive, so any expression that aggregates
	// across regions cannot see it.
	Regions []string
}

// livenessMatcher is one (service, region) liveness target: the selector to match on, and
// the region (if any) to name in the alert.
type livenessMatcher struct {
	sel    string
	region string
}

func (m livenessMatcher) title(svc string) string {
	if m.region == "" {
		return svc
	}
	return svc + " (" + m.region + ")"
}

// livenessMatchers yields one matcher per region for a regional service, or a single
// region-less matcher for a global one.
func (s ServiceScope) livenessMatchers(env string) []livenessMatcher {
	base := fmt.Sprintf(`%s, service_name=%q`, env, s.Metric)
	if len(s.Regions) == 0 {
		return []livenessMatcher{{sel: base}}
	}
	out := make([]livenessMatcher, 0, len(s.Regions))
	for _, r := range s.Regions {
		out = append(out, livenessMatcher{
			sel:    fmt.Sprintf(`%s, region=%q`, base, r),
			region: r,
		})
	}
	return out
}

// ServiceBuiltIns returns the golden per-service alert rules for one env.
//
// # The two tiers, and why the split exists
//
// **Universal signals.** `process_uptime_seconds` is emitted by EVERY service, on every
// export interval, with no traffic and no HTTP (wardnet-cloud's `telemetry::process`). The
// liveness and crash-loop alerts rest on it, and only on it.
//
// That is not a stylistic choice. The RED histogram only produces a series once a request
// has actually been SERVED — so a service with zero traffic has no series at all, and is
// indistinguishable from one that is dead or was never deployed. In production `tunneller`
// sits at zero requests and is simply absent from the RED dashboard: **the one service that
// was once down for forty minutes is the one service a RED-based liveness alert could never
// have seen.** Uptime is what makes "no telemetry ⇒ the process is gone" a sound inference
// rather than a guess about idleness.
//
// **HTTP-only signals.** Error rate and latency come from the RED histogram, so they apply
// only to services that serve HTTP. Two independent guards keep a non-HTTP service from
// alerting on signals it cannot emit:
//
//  1. the rules are only generated when at least one service is eligible; and
//  2. every one of them is grouped `by (service_name)`, so a service with no series
//     produces no group and therefore no alert instance — structurally, not by luck.
//
// # Severity
//
// **Only "the service is not running" is Critical.** Down and CrashLooping page; everything
// else is a Warning you look at in the morning. A larger critical set trains you to ignore
// the pager, which is worse than having none.
func ServiceBuiltIns(env string, services []ServiceScope) []Alert {
	if len(services) == 0 {
		return nil
	}
	e := envMatcher(env)

	services = append([]ServiceScope(nil), services...)
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })

	var anyHTTP, anyMesh, anyDatabase bool
	for _, s := range services {
		anyHTTP = anyHTTP || s.HTTP
		anyMesh = anyMesh || s.Mesh
		anyDatabase = anyDatabase || s.Database
	}

	var alerts []Alert

	// ── Liveness: per service, PER REGION ──
	//
	// The host equivalent (`Host Metrics Missing`) is `count(...) < 1` across the whole env,
	// so it only fires when EVERY host is gone. A per-service `count(...) < 1` repeats that
	// mistake one level down: a regional service runs one instance per region, so a service
	// dying in ONE region leaves the other region's series alive, the count never reaches 0,
	// and a full regional outage is invisible. Prd has one region today, which is the only
	// reason that would have worked at all. So: one rule per (service, region).
	//
	// The expression is `absent_over_time` GATED on the service having been alive recently:
	//
	//	absent_over_time(m[5m]) * on() (count(count_over_time(m[24h])) > 0)
	//
	// Both halves are load-bearing.
	//
	// `absent_over_time` rather than `count(...) < 1`, because a vanished series does not
	// evaluate to zero — it produces NO SERIES, so `< 1` is never true and the rule can only
	// fire via NoData. Worse, Grafana's query node is a RANGE query over the last 600s and
	// Prometheus fills steps from the last sample via its 5m staleness lookback, so the frame
	// keeps returning the dead service's final sample: the rule would not have gone NoData
	// for ~15 minutes, and then still owed its 5m `for` — a "Service Down" alert that takes
	// twenty minutes to fire, sold as the fix for a forty-minute outage. `absent_over_time`
	// flips positive ~5m after the last sample and is a real series the math node can read.
	//
	// The `count_over_time(m[24h]) > 0` gate is what stops a DECLARED-BUT-NEVER-RELEASED
	// service paging Critical forever. `inforge deploy` creates these rules at Pulumi time,
	// but a service's binary only lands later (`inforge releases deploy`) — and an ephemeral
	// preview may skip a workload entirely. Without the gate, every such service pages
	// immediately and never stops. With it, a service that has never reported simply has no
	// right-hand series, the product is empty, and the rule stays silent until the thing has
	// actually run once. This also removes the hard "deploy wardnet-cloud first" ordering
	// hazard: before the process-metrics release ships, no service has ever reported, so
	// nothing fires.
	//
	// The cost is honest and bounded: a service dead for more than 24h stops alerting. By
	// then it is not news.
	//
	// NoDataOK, deliberately: a HEALTHY service makes `absent_over_time` return nothing, so
	// No Data is the normal, correct state here. Relying on NoDataAlerting (as the earlier
	// draft did) would have inverted that.
	for _, s := range services {
		for _, m := range s.livenessMatchers(e) {
			alerts = append(alerts, b(
				"Service Down: "+m.title(s.Name),
				fmt.Sprintf(`absent_over_time(process_uptime_seconds{%s}[5m]) * on() (count(count_over_time(process_uptime_seconds{%s}[24h])) > 0)`, m.sel, m.sel),
				"> 0", "2m", SeverityCritical,
				fmt.Sprintf("%s is not reporting telemetry — the process is gone.", m.title(s.Name)),
				NoDataOK,
			))
		}
	}

	// ── Crash loop ──
	//
	// `resets()` (which `Host Rebooted` uses) does NOT work here: `service.instance.id` is
	// regenerated on every start, so each incarnation is a DISTINCT series — a restart looks
	// like a new series, not a counter reset, and `resets()` sees nothing.
	//
	// Instead: an uptime that keeps being young means the process keeps being replaced. A
	// one-off restart (a deploy) resolves itself as uptime grows past the threshold; a crash
	// loop never does. This is the alert that catches the post-Restart=always face of the
	// outage — a service flapping forever instead of staying dead.
	alerts = append(alerts, b(
		"Service Crash-Looping",
		fmt.Sprintf(`min by (service_name) (process_uptime_seconds{%s})`, e),
		"< 300", "15m", SeverityCritical,
		"{{ $labels.service_name }} keeps restarting (uptime below 5m for 15m).",
		NoDataOK,
	))

	if anyHTTP {
		// The `clamp_min(…, 1)` denominator is a MINIMUM-VOLUME GUARD, not a divide-by-zero
		// nicety. The fleet runs at ~0.1 req/s, so without it a single 500 in a quiet minute
		// is a 100% error rate and pages on noise. Clamping the denominator to 1 req/s means
		// a low-traffic service must produce a real, sustained volume of errors to trip it.
		alerts = append(alerts, b(
			"Service Error Rate High",
			fmt.Sprintf(`100 * sum by (service_name) (rate(http_server_request_duration_seconds_count{%s, http_response_status_code=~"5.."}[5m])) / clamp_min(sum by (service_name) (rate(http_server_request_duration_seconds_count{%s}[5m])), 1)`, e, e),
			"> 5", "15m", SeverityWarning,
			"{{ $labels.service_name }} is returning 5xx for {{ $value }}% of requests.",
			NoDataOK,
		))

		alerts = append(alerts, b(
			"Service Latency High",
			fmt.Sprintf(`histogram_quantile(0.95, sum by (le, service_name) (rate(http_server_request_duration_seconds_bucket{%s}[5m])))`, e),
			"> 1", "10m", SeverityWarning,
			"{{ $labels.service_name }} p95 latency above 1s ({{ $value }}s).",
			NoDataOK,
		))
	}

	if anyMesh {
		// The signal that was missing when it mattered: a caller whose mesh identity is
		// rejected gets 403 on every call to a peer, and before wardnet-cloud metered its
		// outbound calls there was no metric of that anywhere — only log lines.
		//
		// Deliberately WARNING, not critical, and deliberately slow (15m). inforge's own
		// deploy-ordering race (#226) produces a burst of mesh 403s on EVERY deploy: a
		// critical, fast alert here would page on every ship and be muted within a week. The
		// long `for` rides out the deploy window. When #226 is fixed this can tighten.
		//
		// 403 (identity rejected), 5xx (peer or its proxy broken) and `error` (transport:
		// unreachable, TLS refused) mean the mesh path is broken. Other 4xx are the callee's
		// application answering — which proves the mesh WORKS.
		alerts = append(alerts, b(
			"Mesh Calls Failing",
			fmt.Sprintf(`100 * sum by (service_name, mesh_target) (rate(http_client_request_duration_seconds_count{%s, mesh_outcome=~"403|5..|error"}[5m])) / clamp_min(sum by (service_name, mesh_target) (rate(http_client_request_duration_seconds_count{%s}[5m])), 1)`, e, e),
			"> 10", "15m", SeverityWarning,
			"{{ $labels.service_name }} → {{ $labels.mesh_target }}: {{ $value }}% of mesh calls failing (403 / 5xx / unreachable).",
			NoDataOK,
		))
	}

	if anyDatabase {
		// Pool exhaustion is invisible from every other angle: the database is healthy, the
		// host is healthy, the service is up — and every request stalls up to the 5s
		// acquire_timeout waiting for a connection. A latency cliff with no cause anywhere.
		alerts = append(alerts, b(
			"Service DB Pool Saturated",
			fmt.Sprintf(`100 * sum by (service_name) (db_client_connection_count{%s, state="used"}) / clamp_min(sum by (service_name) (db_client_connection_max{%s}), 1)`, e, e),
			"> 80", "10m", SeverityWarning,
			"{{ $labels.service_name }} is using {{ $value }}% of its database connection pool.",
			NoDataOK,
		))
	}

	// FD exhaustion is the classic abrupt death of a socket-heavy service (tunneller holds a
	// descriptor per live tunnel). A leading indicator with time to act, hence Warning.
	alerts = append(alerts, b(
		"Service File Descriptors High",
		fmt.Sprintf(`100 * process_open_file_descriptor_count{%s} / clamp_min(process_open_file_descriptor_limit{%s}, 1)`, e, e),
		"> 80", "10m", SeverityWarning,
		"{{ $labels.service_name }} is using {{ $value }}% of its file-descriptor limit.",
		NoDataOK,
	))

	// DORMANT BY DESIGN until inforge imposes a per-service memory cap (#232).
	//
	// `process_memory_limit_bytes` is read from the service's own cgroup. Today the units set
	// no `MemoryMax=`, so the cgroup reports `max`, wardnet-cloud reports NOTHING, and the
	// vector match below finds no partner series — so this yields no data and never fires.
	// The moment #232 lands, the limit series appears and this starts working, with no rule
	// change. Writing it now (rather than after) is what makes that true.
	alerts = append(alerts, b(
		"Service Memory High",
		fmt.Sprintf(`100 * process_memory_usage_bytes{%s} / clamp_min(process_memory_limit_bytes{%s}, 1)`, e, e),
		"> 85", "10m", SeverityWarning,
		"{{ $labels.service_name }} is using {{ $value }}% of its memory cap.",
		NoDataOK,
	))

	// Leak detection, and the only per-service memory signal that works WITHOUT a cap.
	//
	// Compares the RECENT FLOOR against the LONGER-TERM FLOOR. `delta()` would be wrong here:
	// it is last-minus-first over the window, so a service that takes a traffic burst,
	// climbs 300MB and then sits perfectly FLAT keeps reporting that 300MB delta for the
	// next five hours — firing a "possible leak" alert about memory that is provably not
	// growing. The minima ignore spikes, and a plateau makes both minima equal, so a genuine
	// leak (a floor that keeps rising) is the only thing that trips it.
	alerts = append(alerts, b(
		"Service Memory Growing",
		fmt.Sprintf(`min_over_time(process_memory_usage_bytes{%s}[1h]) - min_over_time(process_memory_usage_bytes{%s}[6h])`, e, e),
		"> 268435456", "1h", SeverityWarning,
		"{{ $labels.service_name }} RSS floor rose by over 256MB in 6h — memory is not being returned (possible leak).",
		NoDataOK,
	))

	return alerts
}
