package grafanaalert

import "fmt"

// CrowdsecBuiltIns returns the built-in CrowdSec alert rules for one env (ADR-0043),
// generated from the CrowdSec agent + firewall-bouncer metrics scraped on edge hosts.
// Like the Postgres built-ins, the caller gates these on CrowdSec actually being enabled
// (security.crowdsec.enabled) so they cannot evaluate on an env that runs no CrowdSec.
//
// Both use NoDataOK, not NoDataAlerting: the exact cs_* metric names are only confirmed
// against a running agent, so ABSENCE (a name mismatch, or CrowdSec not yet scraped) must
// not fire — these alert only on a present-but-bad signal. Once the metric names are
// verified on a real edge, an absence variant can be tightened to NoDataAlerting.
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

		// Bouncer not pulling: the firewall bouncer has not fetched decisions recently, so
		// new bans are not being enforced at the edge. cs_bouncer_last_pull is a unix ts.
		b("CrowdSec Bouncer Not Pulling",
			fmt.Sprintf(`time() - max by (instance) (cs_bouncer_last_pull{%s})`, e),
			"> 600", "10m", SeverityWarning,
			"CrowdSec firewall bouncer on {{ $labels.instance }} has not pulled decisions in over 10m — enforcement is drifting.",
			NoDataOK),
	}
}
