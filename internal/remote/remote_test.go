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

func TestDeleteFileScriptQuotesPath(t *testing.T) {
	script := DeleteFileScript(`/etc/caddy/conf.d/x;rm -rf ~.caddy`)
	assert.True(t, strings.HasPrefix(script, "sudo rm -f '"),
		"delete path must be single-quoted: %q", script)
	assert.Contains(t, script, `'/etc/caddy/conf.d/x;rm -rf ~.caddy'`)
}
