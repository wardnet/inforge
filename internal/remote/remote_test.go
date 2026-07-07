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

// TestWriteFileScriptModeIsAtomicAndRestrictive asserts the write stages to a temp
// (mktemp) and atomically mv's into place — so an interrupted write never truncates
// the live secret file — and that the temp is chmod'd 0600 before the mv, so the
// secret is never momentarily world-readable. The content never appears in argv,
// and the destination is never written directly by tee.
func TestWriteFileScriptModeIsAtomicAndRestrictive(t *testing.T) {
	const dest = "/etc/wardnet/services/ghost/credential.age"
	script := WriteFileScriptMode(dest, "ciphertext", "0600")

	assert.Contains(t, script, "mktemp", "must stage to a temp file")
	assert.Contains(t, script, `base64 -d | sudo tee "$tmp"`, "content is written to the temp, not the live path")
	assert.NotContains(t, script, `tee '`+dest+`'`, "must NOT tee directly into the live destination (non-atomic)")

	chmodIdx := strings.Index(script, "chmod '0600' \"$tmp\"")
	mvIdx := strings.Index(script, `sudo mv "$tmp" '`+dest+`'`)
	assert.GreaterOrEqual(t, chmodIdx, 0, "must chmod the temp to the restrictive mode: %q", script)
	assert.GreaterOrEqual(t, mvIdx, 0, "must atomically mv the temp into place: %q", script)
	assert.Less(t, chmodIdx, mvIdx, "chmod must precede the mv, so the destination is never world-readable")
	assert.NotContains(t, script, "ciphertext")
}

// TestWriteFileScriptDefaultMode asserts the default (no-mode) variant is also
// atomic and applies the historical 0644 mode.
func TestWriteFileScriptDefaultMode(t *testing.T) {
	script := WriteFileScript("/etc/wardnet/services/ghost/descriptor.yaml", "version: 1")
	assert.Contains(t, script, "mktemp")
	assert.Contains(t, script, "chmod '0644'")
	assert.Contains(t, script, `sudo mv "$tmp" '/etc/wardnet/services/ghost/descriptor.yaml'`)
}

func TestDeleteFileScriptQuotesPath(t *testing.T) {
	script := DeleteFileScript(`/etc/caddy/conf.d/x;rm -rf ~.caddy`)
	assert.True(t, strings.HasPrefix(script, "sudo rm -f '"),
		"delete path must be single-quoted: %q", script)
	assert.Contains(t, script, `'/etc/caddy/conf.d/x;rm -rf ~.caddy'`)
}
