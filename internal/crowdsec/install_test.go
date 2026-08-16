package crowdsec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInstallScriptAddsRepoAndInstallsBothPackages(t *testing.T) {
	s := InstallScript("")
	assert.Contains(t, s, "signed-by="+KeyringPath)
	assert.Contains(t, s, "packagecloud.io/crowdsec/crowdsec/${ID}/")
	assert.Contains(t, s, "install -y "+AgentPackage+" "+BouncerPackage)
	assert.Contains(t, s, "systemctl enable "+AgentService+" "+BouncerService)
	// No version pin: the bare agent package, not agent=<ver>.
	assert.NotContains(t, s, AgentPackage+"=")
}

func TestInstallScriptPinsAgentVersionOnly(t *testing.T) {
	s := InstallScript("1.6.4")
	assert.Contains(t, s, "install -y "+AgentPackage+"=1.6.4 "+BouncerPackage)
	// The bouncer is never pinned (independent version scheme).
	assert.NotContains(t, s, BouncerPackage+"=")
}

func TestInstallScriptGivesUnitsARestartPolicy(t *testing.T) {
	s := InstallScript("")
	// A crashed security daemon must self-heal, not silently stop enforcing.
	assert.Contains(t, s, "Restart=always")
	assert.Contains(t, s, "10-restart.conf")
	assert.Contains(t, s, "systemctl daemon-reload")
}

func TestConfigScriptInstallsCollectionsAndRegistersCAPI(t *testing.T) {
	s := ConfigScript()
	assert.Contains(t, s, "cscli collections install crowdsecurity/nginx crowdsecurity/base-http-scenarios crowdsecurity/http-cve")
	// CAPI registration is guarded so a re-deploy of an already-registered host is a no-op.
	assert.Contains(t, s, "cscli capi status >/dev/null 2>&1 || sudo cscli capi register")
	// The acquisition + prometheus overlays are written (base64-transported, so their
	// paths appear in the mv target).
	assert.Contains(t, s, AcquisPath)
	assert.Contains(t, s, AgentConfigLocal)
	// The hub index is refreshed BEFORE collections install, or a fresh host can't find them.
	assert.Contains(t, s, "cscli hub update")
	assert.Less(t, strings.Index(s, "cscli hub update"), strings.Index(s, "cscli collections install"),
		"hub update must precede collections install")
}

func TestAssertScriptRetriesLAPIStatus(t *testing.T) {
	s := AssertScript()
	// Poll before the final unsuppressed check, so a just-restarted LAPI does not race.
	assert.Contains(t, s, "for i in 1 2 3 4 5")
	assert.Contains(t, s, "cscli lapi status")
	assert.Contains(t, s, "cscli bouncers list -o json | grep -q 'inforge-fw'")
}

func TestEnrollScriptSurfacesFailure(t *testing.T) {
	s := EnrollScript("tok")
	assert.Contains(t, s, "cscli console enroll 'tok'")
	// Failure is logged, not silently swallowed by `|| true`.
	assert.NotContains(t, s, "|| true")
	assert.Contains(t, s, ">&2")
}

func TestBouncerScriptIsIdempotentAndRestarts(t *testing.T) {
	s := BouncerScript("KEY123abc")
	assert.Contains(t, s, "cscli bouncers delete 'inforge-fw'")
	assert.Contains(t, s, "cscli bouncers add 'inforge-fw' --key 'KEY123abc'")
	assert.Contains(t, s, BouncerConfigLocal)
	assert.Contains(t, s, "systemctl restart "+BouncerService)
}

func TestAssertScriptChecksLAPIAndBouncer(t *testing.T) {
	s := AssertScript()
	assert.Contains(t, s, "cscli lapi status")
	assert.Contains(t, s, "cscli bouncers list -o json | grep -q 'inforge-fw'")
}

func TestBouncerLocalRendersDeterministicYAML(t *testing.T) {
	const want = `# Managed by inforge (ADR-0043) — CrowdSec firewall bouncer overlay.
mode: nftables
api_url: http://127.0.0.1:8080/
api_key: KEY123abc
deny_action: DROP
disable_ipv6: false
prometheus:
  enabled: true
  listen_addr: 127.0.0.1
  listen_port: 60601
`
	assert.Equal(t, want, bouncerLocal("KEY123abc"))
}

func TestNginxAcquisPointsAtNginxLogs(t *testing.T) {
	a := nginxAcquis()
	assert.Contains(t, a, "/var/log/nginx/access.log")
	assert.Contains(t, a, "/var/log/nginx/error.log")
	assert.Contains(t, a, "type: nginx")
}

func TestAgentPrometheusOverlayBindsLoopback(t *testing.T) {
	p := agentPrometheusLocal()
	assert.Contains(t, p, "enabled: true")
	assert.Contains(t, p, "listen_addr: 127.0.0.1")
	assert.Contains(t, p, "listen_port: 6060")
}

// The bouncer api_key must never appear as plaintext in the config-write step — it is
// base64-transported through the shell (only the cscli argv carries it, a documented
// accepted exposure).
func TestBouncerKeyNotPlaintextInConfigWrite(t *testing.T) {
	s := BouncerScript("SUPERSECRETKEY")
	// The overlay content is base64'd, so the raw key appears exactly once in the whole
	// script: the `cscli bouncers add --key` argv (a documented accepted exposure).
	assert.Equal(t, 1, strings.Count(s, "SUPERSECRETKEY"))
}

// The regression that broke every prd deploy: the sources file interpolated the host's
// own ${VERSION_CODENAME}, and packagecloud publishes no suite for Ubuntu 26.04
// (`resolute`). The suite must be resolved by probing, and it must be a variable the
// probe set — never the raw codename.
func TestInstallScriptResolvesSuiteByProbingNotByCodename(t *testing.T) {
	s := InstallScript("")

	// The deb line names the probed suite, not the host's codename.
	assert.Contains(t, s, `/${ID}/ ${suite} main`)
	assert.NotContains(t, s, `/${ID}/ ${VERSION_CODENAME} main`)

	// The probe hits the suite's Release object and tries the host codename first.
	assert.Contains(t, s, RepoBaseURL+`/${ID}/dists/${candidate}/Release`)
	assert.Contains(t, s, `for candidate in "${VERSION_CODENAME}" "$fallback"`)

	// An unresolvable suite is a hard failure, not a silently-written broken source.
	assert.Contains(t, s, `if [ -z "$suite" ]; then`)
	assert.Contains(t, s, "exit 1")
}

// The probe is worthless if the sources file is written first: apt-get update fails hard
// on an unreachable source, so a broken entry breaks every LATER apt-using step on the
// host (nginx, otelcol, postgres), not just CrowdSec's own.
func TestInstallScriptProbesBeforeWritingTheSourcesFile(t *testing.T) {
	s := InstallScript("")
	probe := strings.Index(s, "dists/${candidate}/Release")
	write := strings.Index(s, "sudo tee "+RepoListPath)
	assert.Positive(t, probe, "probe must be present")
	assert.Positive(t, write, "sources write must be present")
	assert.Less(t, probe, write, "the suite must be probed BEFORE the sources file is written")
}

// A host poisoned by an earlier run must self-heal: the stale sources file has to go
// before the first apt call, or that call fails and the script can never repair it.
func TestInstallScriptClearsAStaleSourcesFileBeforeTheFirstAptCall(t *testing.T) {
	s := InstallScript("")
	rm := strings.Index(s, "sudo rm -f "+RepoListPath)
	// The real invocation, not the word in a comment.
	apt := strings.Index(s, "apt-get -o DPkg::Lock::Timeout")
	assert.Positive(t, rm, "the stale sources file must be removed")
	assert.Positive(t, apt, "an apt invocation must be present")
	assert.Less(t, rm, apt, "removal must precede the first apt call")
}

// The script is a Pulumi trigger: unstable rendering would replace the resource (and
// reinstall CrowdSec) on every deploy for no reason.
func TestInstallScriptRendersDeterministically(t *testing.T) {
	assert.Equal(t, InstallScript("1.7.8"), InstallScript("1.7.8"))
	s := InstallScript("")
	// Fallbacks render in sorted order regardless of map iteration order.
	assert.Less(t, strings.Index(s, "debian) fallback="), strings.Index(s, "ubuntu) fallback="))
}

// The fallbacks must name suites packagecloud actually publishes — the whole point of
// the table. Kept as a documented expectation; bump when packagecloud catches up.
func TestFallbackSuitesCoverOurDeployTargets(t *testing.T) {
	assert.Equal(t, "noble", FallbackSuites["ubuntu"], "Ubuntu deploy targets (26.04 resolute) fall back to noble")
	assert.Equal(t, "bookworm", FallbackSuites["debian"])
}
