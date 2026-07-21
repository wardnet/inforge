package crowdsec

import (
	"fmt"
	"strings"

	"github.com/wardnet/inforge/internal/aptlock"
)

// InstallScript adds the CrowdSec packagecloud apt repository (a pinned signed-by
// keyring, the same shape as internal/nginx's nginx.org repo) and installs the agent +
// nftables firewall bouncer. It is idempotent: the keyring is written once, the repo
// list is rewritten in place, and `apt-get install` is a no-op when both packages are
// already current. agentVersion pins the AGENT only (empty = the repo's current pair) —
// the bouncer versions independently (0.0.x vs the agent's 1.x), so one pin cannot cover
// both. The first apt call is an update (through aptlock, retrying a held lists lock) so
// a fresh image never installs against a missing index.
func InstallScript(agentVersion string) string {
	agentPkg := AgentPackage
	if agentVersion != "" {
		agentPkg = fmt.Sprintf("%s=%s", AgentPackage, agentVersion)
	}
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

%[1]s
sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y curl ca-certificates gnupg

. /etc/os-release

# CrowdSec packagecloud signing key (armored .asc used directly via signed-by, no dearmor —
# which would fail on a host with no controlling tty). Download to a temp then install into
# place so a curl failure never leaves a partial keyring the presence guard would trust.
if [ ! -f %[2]s ]; then
  asc=$(mktemp)
  curl -fsSL %[3]s -o "$asc"
  sudo install -m 0644 "$asc" %[2]s
  rm -f "$asc"
fi
echo "deb [signed-by=%[2]s] https://packagecloud.io/crowdsec/crowdsec/${ID}/ ${VERSION_CODENAME} main" \
  | sudo tee %[4]s >/dev/null

%[1]s
sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y %[5]s %[6]s

# A dead security daemon silently stops enforcing (a crashed bouncer = no bans applied).
# The packaged units may carry no restart policy, so guarantee one via a drop-in — the same
# policy inforge writes for every unit it owns (.agents/rules/daemon-units-restart-on-any-exit.md).
for unit in %[7]s %[8]s; do
  sudo install -d -m 0755 "/etc/systemd/system/${unit}.service.d"
  sudo tee "/etc/systemd/system/${unit}.service.d/10-restart.conf" >/dev/null <<'UNIT'
# Managed by inforge — a daemon has no correct exit.
[Unit]
StartLimitIntervalSec=0

[Service]
Restart=always
RestartSec=5
UNIT
done
sudo systemctl daemon-reload
sudo systemctl enable %[7]s %[8]s
`, aptlock.UpdateCmd(), KeyringPath, RepoKeyURL, RepoListPath, agentPkg, BouncerPackage, AgentService, BouncerService)
}

// ConfigScript writes the agent overlays (prometheus config.yaml.local + the nginx
// acquisition drop-in), installs the hub collections, registers to the Central API for
// the community blocklist, and reloads the agent. Every step is idempotent: the files are
// rewritten, `cscli collections install` is a no-op/upgrade when present, and CAPI
// registration is skipped when already registered. Collection install and CAPI
// registration reach the CrowdSec hub/central API (a deploy-time network dependency).
func ConfigScript() string {
	return strings.Join([]string{
		"set -e",
		writeFileScript(AgentConfigLocal, agentPrometheusLocal(), "0644", "root:root"),
		writeFileScript(AcquisPath, nginxAcquis(), "0644", "root:root"),
		// Refresh the hub index first — a fresh host has none, and `collections install`
		// fails ("can't find collection") without it, failing the deploy under set -e.
		"sudo cscli hub update",
		fmt.Sprintf("sudo cscli collections install %s", strings.Join(Collections, " ")),
		// Register to the Central API (the community blocklist) unless already registered.
		"sudo cscli capi status >/dev/null 2>&1 || sudo cscli capi register",
		// Reload to pick up the new acquisition + collections; restart if reload is unsupported.
		fmt.Sprintf("sudo systemctl reload %s 2>/dev/null || sudo systemctl restart %s", AgentService, AgentService),
	}, "\n")
}

// BouncerScript registers the inforge-owned bouncer key with the local API and writes the
// bouncer's `.local` overlay (nftables mode, the LAPI URL, the api_key, DROP action, and
// its loopback prometheus endpoint), then restarts the bouncer. apiKey must be
// YAML-safe (alphanumeric) — the program mints it with random.RandomPassword(special=off)
// — since it is written unquoted into the overlay. `cscli bouncers add` errors when the
// name already exists, so a delete-then-add makes re-deploys idempotent; the same key is
// rewritten into the overlay in this pass. The key does appear briefly in argv on
// `cscli bouncers add --key`; it is a host-local capability credential and the host is
// already root-trusted, an accepted minor exposure (cscli offers no stdin key input).
func BouncerScript(apiKey string) string {
	return strings.Join([]string{
		"set -e",
		fmt.Sprintf("sudo cscli bouncers delete %s >/dev/null 2>&1 || true", shQuote(BouncerName)),
		fmt.Sprintf("sudo cscli bouncers add %s --key %s", shQuote(BouncerName), shQuote(apiKey)),
		writeFileScript(BouncerConfigLocal, bouncerLocal(apiKey), "0600", "root:root"),
		fmt.Sprintf("sudo systemctl restart %s", BouncerService),
	}, "\n")
}

// EnrollScript enrolls the agent to the CrowdSec console with the given token and
// restarts the agent. It is optional (gated on the reserved crowdsec_enroll secret).
// Console enrollment is non-fatal (an already-enrolled host re-enrolls with an error we
// tolerate), but a failure is SURFACED to the deploy log rather than silently swallowed —
// so a bad or expired token is visible instead of a green deploy that never enrolled.
func EnrollScript(token string) string {
	return strings.Join([]string{
		"set -e",
		fmt.Sprintf(`sudo cscli console enroll %s || echo "inforge: cscli console enroll failed (already enrolled, or a bad/expired token) — verify with 'cscli console status'" >&2`, shQuote(token)),
		fmt.Sprintf("sudo systemctl restart %s", AgentService),
	}, "\n")
}

// AssertScript fails the deploy when CrowdSec did not come up: the agent's local API must
// answer, and the inforge bouncer must be registered with it. It is the deploy-time
// liveness gate (CrowdSec otherwise fails silently — ADR-0043).
func AssertScript() string {
	return strings.Join([]string{
		"set -e",
		// The local API needs a moment to rebind :8080 after the restart above, so poll it
		// briefly before asserting — otherwise a healthy CrowdSec fails the deploy on a race.
		"for i in 1 2 3 4 5; do sudo cscli lapi status >/dev/null 2>&1 && break; sleep 2; done",
		"sudo cscli lapi status", // final, unsuppressed — fails the deploy if still down
		fmt.Sprintf("sudo cscli bouncers list -o json | grep -q %s", shQuote(BouncerName)),
	}, "\n")
}

// agentPrometheusLocal is the agent config.yaml overlay enabling the loopback prometheus
// endpoint the host collector scrapes.
func agentPrometheusLocal() string {
	return fmt.Sprintf(`# Managed by inforge (ADR-0043) — CrowdSec agent prometheus overlay.
prometheus:
  enabled: true
  level: full
  listen_addr: %s
  listen_port: %d
`, AgentMetricsAddr, AgentMetricsPort)
}

// nginxAcquis is the acquisition drop-in pointing CrowdSec at the ingress nginx logs.
func nginxAcquis() string {
	return `# Managed by inforge (ADR-0043) — CrowdSec nginx log acquisition.
source: file
filenames:
  - /var/log/nginx/access.log
  - /var/log/nginx/error.log
labels:
  type: nginx
`
}

// bouncerLocal is the firewall bouncer's config overlay. apiKey is written unquoted, so
// the caller must supply a YAML-safe (alphanumeric) key.
func bouncerLocal(apiKey string) string {
	return fmt.Sprintf(`# Managed by inforge (ADR-0043) — CrowdSec firewall bouncer overlay.
mode: nftables
api_url: %s
api_key: %s
deny_action: DROP
disable_ipv6: false
prometheus:
  enabled: true
  listen_addr: %s
  listen_port: %d
`, LAPIURL, apiKey, BouncerMetricsAddr, BouncerMetricsPort)
}
