package caddy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
)

func TestCaddyfileImportsConfDir(t *testing.T) {
	out := Caddyfile()
	assert.Contains(t, out, "import conf.d/*.caddy")
	// The base Caddyfile declares no sites of its own — every site is imported.
	assert.NotContains(t, out, "reverse_proxy")
}

func TestVhostRendersReverseProxy(t *testing.T) {
	out := Vhost(types.Vhost{Service: "api", FQDN: "api.prd.use1.wardnet.network", Port: 8080})

	assert.Contains(t, out, "api.prd.use1.wardnet.network {")
	assert.Contains(t, out, "reverse_proxy localhost:8080")
	assert.Contains(t, out, "}")
	// No explicit tls directive needed: a bare hostname site gets automatic
	// HTTPS (ACME) from Caddy, so ingress always terminates TLS.
}

func TestVhostPathUsesServiceName(t *testing.T) {
	assert.Equal(t, "api.caddy", VhostFilename("api"))
	assert.Equal(t, "/etc/caddy/conf.d/api.caddy", VhostPath("api"))
}

// TestInstallScriptAptOrder guards the regression that motivated this work: the
// first apt-get invocation must be `update`, never `install`. The infra repo's
// cloud-init ran `apt-get install` before any update and aborted first boot.
func TestInstallScriptAptOrder(t *testing.T) {
	script := InstallScript()
	require.True(t, strings.HasPrefix(script, "#!/usr/bin/env bash"), "must be a bash script")
	assert.Contains(t, script, "set -euo pipefail")

	firstUpdate := strings.Index(script, "apt-get update")
	firstInstall := strings.Index(script, "apt-get install")
	require.NotEqual(t, -1, firstUpdate, "script must run apt-get update")
	require.NotEqual(t, -1, firstInstall, "script must run apt-get install")
	assert.Less(t, firstUpdate, firstInstall,
		"apt-get update must come before the first apt-get install (the apt-order fix)")
}

func TestInstallScriptInstallsTooling(t *testing.T) {
	script := InstallScript()
	// Caddy plus the tools the service bootstrapper relies on.
	for _, tool := range []string{"caddy", "jq", "age", "yq", "sops"} {
		assert.Contains(t, script, tool, "install script should provision %q", tool)
	}
	// Prepares the conf.d directory the base Caddyfile imports from.
	assert.Contains(t, script, ConfDir)
}

// TestInstallScriptVerifiesDownloads guards that the binaries fetched outside
// apt (yq, sops) are checksum-verified before install — a tampered or corrupted
// download must not reach /usr/local/bin.
func TestInstallScriptVerifiesDownloads(t *testing.T) {
	script := InstallScript()
	assert.Contains(t, script, "sha256sum -c -",
		"yq/sops downloads must be checksum-verified")
	assert.Contains(t, script, "YQ_SHA256=", "yq checksum must be pinned")
	assert.Contains(t, script, "SOPS_SHA256=", "sops checksum must be pinned")
}
