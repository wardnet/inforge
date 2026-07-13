package grafanaalert

import (
	"strings"
	"testing"
)

func scopes() []ServiceScope {
	return []ServiceScope{
		// regional: one instance per region, so liveness must be per (service, region)
		{Name: "ddns", Metric: "wardnet-ddns", HTTP: true, Mesh: true, Database: true, Regions: []string{"euc", "use1"}},
		// global: a single instance, no region to distinguish
		{Name: "tenants", Metric: "wardnet-tenants", HTTP: true, Mesh: true, Database: true},
	}
}

// worker is the case the whole two-tier design exists for: a service that serves no HTTP and
// therefore emits no RED histogram. It must still be monitored for liveness — and must never
// be alerted on signals it cannot produce.
func worker() []ServiceScope {
	return []ServiceScope{{Name: "worker", Metric: "wardnet-worker"}}
}

func names(alerts []Alert) []string {
	out := make([]string, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, a.Rule.Name)
	}
	return out
}

func find(t *testing.T, alerts []Alert, name string) Alert {
	t.Helper()
	for _, a := range alerts {
		if a.Rule.Name == name {
			return a
		}
	}
	t.Fatalf("alert %q not generated; got %v", name, names(alerts))
	return Alert{}
}

// expr returns the rule's PromQL, which lives inside the opaque query-model JSON rather than
// on a field of its own — so the quotes in it are JSON-escaped.
func expr(a Alert) string {
	return a.Rule.Data[0].Model
}

func has(alerts []Alert, name string) bool {
	for _, a := range alerts {
		if a.Rule.Name == name {
			return true
		}
	}
	return false
}

// Only "the service is not running" pages. A larger critical set trains you to ignore the
// pager, which is worse than having none — so this is a contract, not a preference.
func TestOnlyLivenessAlertsAreCritical(t *testing.T) {
	critical := map[string]bool{}
	for _, a := range ServiceBuiltIns("prd", scopes()) {
		if a.Severity == SeverityCritical {
			critical[a.Rule.Name] = true
		}
	}
	want := map[string]bool{
		"Service Down: ddns (euc)":  true,
		"Service Down: ddns (use1)": true,
		"Service Down: tenants":     true,
		"Service Crash-Looping":     true,
		// The third member, and the only one that is not "the process is gone". It earns its
		// place because #226 removed the one BENIGN cause of a sustained mesh failure (the
		// deploy-ordering race that 403'd on every ship). What is left never self-heals — a
		// rejected identity, a stale allow-map, a broken trust chain — and a service that
		// cannot call its only dependency is as broken as one that is down.
		"Mesh Calls Failing": true,
	}
	if len(critical) != len(want) {
		t.Fatalf("critical set drifted.\n got: %v\nwant: %v", critical, want)
	}
	for name := range want {
		if !critical[name] {
			t.Errorf("%q must be critical", name)
		}
	}
}

// The mesh alert pages, and only because #226 is fixed.
//
// Before it, inforge's own deploy-ordering race produced a burst of mesh 403s on EVERY
// deploy, so this had to be a 15m Warning or it would have paged on every ship and been
// muted within a week. With the race gone, a sustained mesh failure has no benign cause left:
// a rejected identity, a stale allow-map, a broken trust chain and an unroutable peer all
// PERSIST until someone acts. A service that is up but cannot call its only dependency is as
// broken as one that is down.
//
// If this is ever reverted to a Warning, check whether the deploy race came back first.
func TestMeshAlertPagesButStillAbsorbsACalleeRestart(t *testing.T) {
	a := find(t, ServiceBuiltIns("prd", scopes()), "Mesh Calls Failing")
	if a.Severity != SeverityCritical {
		t.Error("a caller that cannot reach its peer is not doing its job, and it will not self-heal")
	}
	// Still long enough to absorb the seconds-long blip while a callee restarts during its
	// own release — the only remaining transient cause.
	if a.Rule.For != "5m" {
		t.Errorf("for = %q; must still ride out a callee's restart, just not a deploy-wide 403 burst", a.Rule.For)
	}
}

// The core of the two-tier model. A service that serves no HTTP emits no RED histogram, so it
// must get the universal liveness alerts and NOTHING that depends on request metrics.
func TestANonHTTPServiceGetsLivenessButNoHTTPAlerts(t *testing.T) {
	alerts := ServiceBuiltIns("prd", worker())

	if !has(alerts, "Service Down: worker") || !has(alerts, "Service Crash-Looping") {
		t.Fatal("a worker must still be monitored for liveness — that is the whole point of process.uptime")
	}
	for _, forbidden := range []string{"Service Error Rate High", "Service Latency High"} {
		if has(alerts, forbidden) {
			t.Errorf("%q generated for a non-HTTP service: it emits no RED histogram, so the rule could only sit at No Data forever", forbidden)
		}
	}
	// Not a mesh member and holds no database grant.
	for _, forbidden := range []string{"Mesh Calls Failing", "Service DB Pool Saturated"} {
		if has(alerts, forbidden) {
			t.Errorf("%q generated for a service that cannot emit its signal", forbidden)
		}
	}
}

// Liveness is PER SERVICE, deliberately. The host equivalent (`Host Metrics Missing`) is a
// fleet-wide `count(...) < 1`, which only fires when EVERY host is gone — the service
// equivalent of that would not have caught one service dying, which is exactly what happened.
func TestLivenessIsPerServiceAndPerRegion(t *testing.T) {
	alerts := ServiceBuiltIns("prd", scopes())

	// A regional service runs one instance PER REGION. Any expression that aggregates across
	// regions cannot see it die in ONE of them — the surviving region keeps the aggregate
	// alive. That is the fleet-wide-count blind spot, moved one level down.
	for _, region := range []string{"euc", "use1"} {
		a := find(t, alerts, "Service Down: ddns ("+region+")")
		if !strings.Contains(expr(a), `region=\"`+region+`\"`) {
			t.Errorf("ddns/%s: the rule must pin its region, got: %s", region, expr(a))
		}
		if !strings.Contains(expr(a), `service_name=\"wardnet-ddns\"`) {
			t.Errorf("ddns/%s: the rule must pin its service, got: %s", region, expr(a))
		}
	}

	// A global service has one instance and no region to distinguish.
	g := find(t, alerts, "Service Down: tenants")
	if strings.Contains(expr(g), "region=") {
		t.Errorf("a global service has no region label to pin: %s", expr(g))
	}
}

// `count(...) < 1` cannot work: a vanished series does not evaluate to zero, it produces NO
// SERIES — so the condition is never true and the rule can only fire via NoData. Grafana's
// query node is a RANGE query and Prometheus fills steps from the last sample via its 5m
// staleness lookback, so the frame keeps returning the DEAD service's final sample for ~15
// minutes. A "Service Down" alert that takes twenty minutes to fire, sold as the fix for a
// forty-minute outage.
func TestLivenessUsesAbsentOverTimeNotACount(t *testing.T) {
	a := find(t, ServiceBuiltIns("prd", scopes()), "Service Down: tenants")
	if !strings.Contains(expr(a), "absent_over_time(") {
		t.Errorf("liveness must use absent_over_time — a vanished series is not a zero: %s", expr(a))
	}
	if strings.Contains(expr(a), "count(process_uptime_seconds") {
		t.Error("count(...) < 1 can never be true for a vanished series")
	}
	// A HEALTHY service makes absent_over_time return nothing, so No Data is the NORMAL state
	// here. NoDataAlerting would invert that and page for every healthy service.
	if a.Rule.NoDataState != NoDataOK {
		t.Error("with absent_over_time, No Data is the healthy state — alerting on it inverts the rule")
	}
}

// The gate that stops a declared-but-never-released service paging Critical forever, and
// removes the hard "deploy wardnet-cloud first" ordering hazard: inforge creates these rules
// at Pulumi time, but the binary lands later (`releases deploy`) — and an ephemeral preview
// may skip a workload entirely. A service that has NEVER reported has no right-hand series,
// so the product is empty and the rule stays silent until the thing has actually run once.
func TestLivenessOnlyFiresForAServiceThatHasActuallyRun(t *testing.T) {
	a := find(t, ServiceBuiltIns("prd", scopes()), "Service Down: tenants")
	if !strings.Contains(expr(a), "count_over_time(") {
		t.Errorf("without a has-ever-reported gate, a never-released service pages forever: %s", expr(a))
	}
}

// Liveness rests on process.uptime and nothing else. The RED histogram produces no series at
// all for a service that has served no requests, so a zero-traffic service is indistinguishable
// from a dead one — in production `tunneller` sits at zero requests and is absent from the RED
// dashboard entirely. Building liveness on request metrics would have missed the real outage.
func TestLivenessRestsOnUptimeNotOnRequestMetrics(t *testing.T) {
	alerts := ServiceBuiltIns("prd", scopes())
	for _, name := range []string{"Service Down: tenants", "Service Crash-Looping"} {
		e := expr(find(t, alerts, name))
		if !strings.Contains(e, "process_uptime_seconds") {
			t.Errorf("%s: liveness must rest on process_uptime_seconds, got: %s", name, e)
		}
		if strings.Contains(e, "http_server_request_duration") {
			t.Errorf("%s: a zero-traffic service has NO request series — it would look dead", name)
		}
	}
}

// `resets()` (what `Host Rebooted` uses) cannot work here: service.instance.id is regenerated
// per start, so a restart is a NEW SERIES, not a counter reset. Uptime staying young is the
// only thing that sees a crash loop.
func TestCrashLoopDoesNotUseResets(t *testing.T) {
	a := find(t, ServiceBuiltIns("prd", scopes()), "Service Crash-Looping")
	if strings.Contains(expr(a), "resets(") {
		t.Error("resets() cannot see a restart: each incarnation is a distinct series (instance.id churns)")
	}
	if !strings.Contains(expr(a), "min by (service_name)") {
		t.Errorf("must detect a persistently-young uptime, got: %s", expr(a))
	}
}

// The fleet runs at ~0.1 req/s. Without a clamped denominator, ONE 500 in a quiet minute is a
// 100% error rate — the alert would page on noise and be muted.
func TestRatioAlertsGuardAgainstLowVolume(t *testing.T) {
	alerts := ServiceBuiltIns("prd", scopes())
	for _, name := range []string{"Service Error Rate High", "Mesh Calls Failing", "Service DB Pool Saturated"} {
		a := find(t, alerts, name)
		if !strings.Contains(expr(a), "clamp_min(") {
			t.Errorf("%s: a ratio needs a minimum-volume guard, got: %s", name, expr(a))
		}
	}
}

// The memory-saturation rule is DORMANT BY DESIGN: no unit sets MemoryMax= yet (#232), so the
// limit series does not exist and the vector match finds no partner. Writing it now is what
// makes it start working the moment the cap lands, with no rule change.
func TestMemorySaturationIsWrittenAgainstTheCapThatDoesNotExistYet(t *testing.T) {
	a := find(t, ServiceBuiltIns("prd", scopes()), "Service Memory High")
	if !strings.Contains(expr(a), "process_memory_limit_bytes") {
		t.Errorf("must be a ratio against the cgroup cap, got: %s", expr(a))
	}
	if a.Rule.NoDataState != NoDataOK {
		t.Error("while no cap exists there is no series — that must resolve to OK, not page")
	}
}

func TestNoServicesGeneratesNoRules(t *testing.T) {
	if got := ServiceBuiltIns("prd", nil); got != nil {
		t.Errorf("an env with no services must generate no service rules, got %v", names(got))
	}
}

// Every generated rule must be scoped to its environment, or a prd alert would fire on stg
// data (and vice versa).
func TestEveryRuleIsEnvScoped(t *testing.T) {
	for _, a := range ServiceBuiltIns("prd", scopes()) {
		if !strings.Contains(expr(a), `deployment_environment_name=\"prd\"`) {
			t.Errorf("%s is not env-scoped: %s", a.Rule.Name, expr(a))
		}
	}
}

// `delta()` is last-minus-first over the window, so it CANNOT see a plateau: a service that
// takes a traffic burst, climbs 300MB and then sits perfectly flat keeps reporting that
// 300MB delta for the next five hours — firing "possible leak" about memory that is provably
// not growing. Comparing the recent floor against the longer-term floor makes a plateau
// equalise the two minima, so only a genuinely rising floor trips it.
func TestMemoryGrowthComparesFloorsSoAPlateauDoesNotFire(t *testing.T) {
	a := find(t, ServiceBuiltIns("prd", scopes()), "Service Memory Growing")
	if strings.Contains(expr(a), "delta(") {
		t.Error("delta() fires on any step and cannot see a plateau — it contradicts the rule's own summary")
	}
	if !strings.Contains(expr(a), "min_over_time(") {
		t.Errorf("a leak is a RISING FLOOR, not a step: %s", expr(a))
	}
}
