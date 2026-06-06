package remote

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQuoteNeutralizesMetacharacters(t *testing.T) {
	// A value carrying shell metacharacters must come back single-quoted with no
	// way to break out, so $(...) / backticks / ; never reach the shell live.
	got := Quote(`api$(touch /tmp/pwned)`)
	assert.Equal(t, `'api$(touch /tmp/pwned)'`, got)

	// An embedded single quote is closed, escaped, and reopened.
	assert.Equal(t, `'a'\''b'`, Quote(`a'b`))
}

// TestWriteFileScriptQuotesPath guards the remote-command-injection fix: the
// path (which embeds a caller-supplied name) must be single-quoted, not
// interpolated raw or via Go's shell-unsafe %q. Without this a name like
// `x$(reboot)` would execute on the host as root.
func TestWriteFileScriptQuotesPath(t *testing.T) {
	script := WriteFileScript(`/etc/caddy/conf.d/x$(reboot).caddy`, "irrelevant content")

	// The dangerous path appears only inside single quotes — never bare.
	assert.Contains(t, script, `'/etc/caddy/conf.d/x$(reboot).caddy'`)
	assert.NotContains(t, script, `"/etc/caddy/conf.d/x$(reboot).caddy"`,
		"path must not be double-quoted (the shell would expand $(...))")
	// The content is delivered base64-encoded, never inlined raw.
	assert.NotContains(t, script, "irrelevant content")
	assert.Contains(t, script, "base64 -d")
}

// TestWriteFileScriptModeSetsRestrictivePermsBeforeWrite asserts a 0600 file is
// created with the restrictive mode before content is teed in, so a secret
// credential is never momentarily world-readable. The mode is single-quoted.
func TestWriteFileScriptModeSetsRestrictivePermsBeforeWrite(t *testing.T) {
	script := WriteFileScriptMode("/etc/wardnet/services/ghost/credential.age", "ciphertext", "0600")

	installIdx := strings.Index(script, "install -m '0600' /dev/null '/etc/wardnet/services/ghost/credential.age'")
	teeIdx := strings.Index(script, "base64 -d | sudo tee")
	assert.GreaterOrEqual(t, installIdx, 0, "must pre-create the file at 0600: %q", script)
	assert.Less(t, installIdx, teeIdx, "chmod/install must precede the tee write")
	assert.NotContains(t, script, "ciphertext")
}

// TestWriteFileScriptDefaultHasNoModeInstall asserts the default (no-mode)
// variant does not pre-create the file (preserving the historical 0644 path).
func TestWriteFileScriptDefaultHasNoModeInstall(t *testing.T) {
	script := WriteFileScript("/etc/wardnet/services/ghost/descriptor.yaml", "version: 1")
	assert.NotContains(t, script, "install -m")
}

func TestDeleteFileScriptQuotesPath(t *testing.T) {
	script := DeleteFileScript(`/etc/caddy/conf.d/x;rm -rf ~.caddy`)
	assert.True(t, strings.HasPrefix(script, "sudo rm -f '"),
		"delete path must be single-quoted: %q", script)
	assert.Contains(t, script, `'/etc/caddy/conf.d/x;rm -rf ~.caddy'`)
}
