package grafanaalert

import "fmt"

// CrowdsecBuiltIns returns the built-in CrowdSec alert rules for one env (ADR-0043),
// generated from the CrowdSec agent + firewall-bouncer metrics scraped on edge hosts.
// Like the Postgres built-ins, the caller gates these on CrowdSec actually being enabled
// (security.crowdsec.enabled) so they cannot evaluate on an env that runs no CrowdSec.
//
// Both metric names are confirmed against a real host (crowdsec 1.7.8 +
// crowdsec-firewall-bouncer-nftables 0.0.34): the agent exposes cs_parser_hits_total on
// :6060 and the bouncer exposes lapi_requests_total on :60601. Note the bouncer's series
// are prefixed fw_bouncer_*, NOT cs_bouncer_*, and it publishes no last-pull timestamp —
// its LAPI call counter is the pull-freshness signal instead.
//
// Both still use NoDataOK, not NoDataAlerting: an edge that is scraped but not yet
// reporting (or an env between deploys) must not page. These alert only on a
// present-but-bad signal; absence is covered by the collector's own up/health signal.
func CrowdsecBuiltIns(env string) []Alert {
	e := envMatcher(env)
	return []Alert{
		// Acquisition stalled: CrowdSec is present but parsing ~no log lines, so it is
		// blind to attacks. A genuinely idle edge can also read zero — the threshold is
		// fixed (like every built-in); opt out and re-author if that is expected.
		b("CrowdSec Acquisition Stalled",
			fmt.Sprintf(`sum by (instance) (rate(cs_parser_hits_total{%s}[10m]))`, e),
			"< 0.001", "15m", SeverityWarning,
			"CrowdSec on {{ $labels.instance }} is parsing no log lines — acquisition has stalled (it is blind to attacks).",
			NoDataOK),

		// Bouncer not pulling: the firewall bouncer has stopped calling the local API, so new
		// bans are not reaching nftables and enforcement is drifting. The bouncer polls
		// /v1/decisions/stream continuously, so a flat-zero request rate is the stall signal
		// (it publishes no last-pull timestamp to compare against time()).
		b("CrowdSec Bouncer Not Pulling",
			fmt.Sprintf(`sum by (instance) (rate(lapi_requests_total{%s}[10m]))`, e),
			"< 0.001", "10m", SeverityWarning,
			"CrowdSec firewall bouncer on {{ $labels.instance }} has not called the local API in over 10m — new bans are not being enforced.",
			NoDataOK),
	}
}
