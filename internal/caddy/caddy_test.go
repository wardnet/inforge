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
	assert.Contains(t, script, "caddy", "install script should provision caddy")
	// Prepares the conf.d directory the base Caddyfile imports from.
	assert.Contains(t, script, ConfDir)
}

// TestInstallScriptInstallsNoSecretTooling guards that the host gets no
// jq/yq/sops/age: secrets are fetched at runtime by the Go inforge-bootstrap,
// which decrypts in-process, so the host needs none of them.
func TestInstallScriptInstallsNoSecretTooling(t *testing.T) {
	script := InstallScript()
	for _, tool := range []string{"sops", "yq", "getsops", "mikefarah", "SHA256"} {
		assert.NotContains(t, script, tool, "install script must not provision %q", tool)
	}
}
