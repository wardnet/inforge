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

func TestConfigScriptInstallsCollectionsAndRegistersCAPI(t *testing.T) {
	s := ConfigScript()
	assert.Contains(t, s, "cscli collections install crowdsecurity/nginx crowdsecurity/base-http-scenarios crowdsecurity/http-cve")
	// CAPI registration is guarded so a re-deploy of an already-registered host is a no-op.
	assert.Contains(t, s, "cscli capi status >/dev/null 2>&1 || sudo cscli capi register")
	// The acquisition + prometheus overlays are written (base64-transported, so their
	// paths appear in the mv target).
	assert.Contains(t, s, AcquisPath)
	assert.Contains(t, s, AgentConfigLocal)
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
