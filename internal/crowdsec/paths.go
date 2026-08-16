// Package crowdsec renders the on-host install shell and configuration for the CrowdSec
// agent + nftables firewall bouncer that inforge installs on public edge hosts
// (ADR-0043). It is deploy-side only, stdlib-only, and holds no provider/Pulumi
// dependencies — the program wires it to edge hosts, exactly as internal/nginx and
// internal/otelcol are wired. The rendering is pure (config/params in, shell string out)
// and deterministic, so the same inputs always produce the same bytes.
package crowdsec

const (
	// AgentPackage / BouncerPackage are the apt packages installed from the CrowdSec
	// packagecloud repository. BouncerService/AgentService are their systemd units.
	AgentPackage   = "crowdsec"
	BouncerPackage = "crowdsec-firewall-bouncer-nftables"
	AgentService   = "crowdsec"
	BouncerService = "crowdsec-firewall-bouncer"

	// LAPIURL is the loopback local API the agent serves and the bouncer authenticates to.
	LAPIURL = "http://127.0.0.1:8080/"

	// BouncerName is the stable inforge-owned bouncer identity registered with the LAPI.
	BouncerName = "inforge-fw"

	// On-host config paths. inforge writes `.local` overlays and an acquis.d drop-in
	// rather than editing the packaged files, so a package upgrade never reverts our
	// config (CrowdSec merges *.local over the base config, and reads acquis.d/*.yaml).
	AgentConfigLocal   = "/etc/crowdsec/config.yaml.local"
	AcquisPath         = "/etc/crowdsec/acquis.d/nginx.yaml"
	BouncerConfigLocal = "/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml.local"

	// Prometheus metrics endpoints (loopback only), scraped by the host otelcol collector
	// via a prometheus receiver (ADR-0043, mirroring the ADR-0037 postgresql receiver).
	AgentMetricsAddr   = "127.0.0.1"
	AgentMetricsPort   = 6060
	BouncerMetricsAddr = "127.0.0.1"
	BouncerMetricsPort = 60601

	// packagecloud apt repo, added with a pinned signed-by keyring exactly as
	// internal/nginx adds the nginx.org repo (an armored .asc used directly, no dearmor).
	RepoBaseURL  = "https://packagecloud.io/crowdsec/crowdsec"
	RepoKeyURL   = RepoBaseURL + "/gpgkey"
	KeyringPath  = "/usr/share/keyrings/crowdsec_crowdsec-archive-keyring.asc"
	RepoListPath = "/etc/apt/sources.list.d/crowdsec.list"

	// ReservedNamespace/EnrollSecretKey locate the optional console enrollment token in
	// the env's secrets.enc.yaml — an inforge reserved secret (ADR-0043), mirroring
	// otelcol.AuthSecretNamespace/AuthSecretKey. The community blocklist needs no secret.
	ReservedNamespace = "security"
	EnrollSecretKey   = "crowdsec_enroll" // #nosec G101 -- a lookup key name, not a credential
)

// FallbackSuites maps a distro ID (/etc/os-release ID) to the newest packagecloud
// suite CrowdSec is known to publish for it. The install probes the host's OWN
// ${VERSION_CODENAME} first and only falls back to this when packagecloud serves no
// suite under that name.
//
// This exists because packagecloud lags new distro releases by months, and CrowdSec
// packages are suite-generic — the noble build runs fine on a later Ubuntu. Deploy
// targets moved to Ubuntu 26.04 (`resolute`) and every edge install broke: the script
// wrote a sources file for a suite that 404s, and since `apt-get update` fails hard on
// an unreachable source, it took every LATER apt-using provisioning step on that host
// (nginx, otelcol, postgres) down with it — long after CrowdSec itself was disabled.
//
// Verified against packagecloud (Release object reachable):
//
//	ubuntu: noble 302, jammy 302, questing 404, resolute 404
//	debian: bookworm 302, bullseye 302, trixie 404
//
// Bump an entry when packagecloud starts publishing a newer suite. An ID absent here
// gets no fallback: the install fails loudly rather than guessing a suite name.
var FallbackSuites = map[string]string{
	"ubuntu": "noble",
	"debian": "bookworm",
}

// Collections are the CrowdSec hub items installed on an nginx edge: nginx access/error
// log parsing + scenarios, the generic HTTP attack scenarios, and known-CVE probing.
// They are installed imperatively by cscli, which fetches them from the CrowdSec hub —
// a deliberate network dependency at deploy time (ADR-0043).
var Collections = []string{
	"crowdsecurity/nginx",
	"crowdsecurity/base-http-scenarios",
	"crowdsecurity/http-cve",
}
