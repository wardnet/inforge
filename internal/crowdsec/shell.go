package crowdsec

import (
	"encoding/base64"
	"fmt"
	"path"
	"strings"
)

// shQuote single-quotes s for safe interpolation into a POSIX shell command, escaping
// any embedded single quote as the standard '\'' sequence. The package renders host
// shell but stays Pulumi-free (pure, like internal/nginx and internal/otelcol), so it
// carries its own minimal quoting rather than importing the remote helpers.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// base64Encode returns the standard base64 of s, for transporting arbitrary bytes
// (including a secret) through a shell command without exposing them in argv.
func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// writeFileScript renders sudo shell that writes content to absPath with the given mode
// and owner, creating the parent dir. content is base64-encoded for transport so
// arbitrary bytes (including a secret) survive intact and never appear in argv as
// plaintext. The write stages to a temp then atomic-mv's into place (safe on coreutils 9
// where `install /dev/stdin <existing-file>` fails), mirroring internal/otelcol.
func writeFileScript(absPath, content, mode, owner string) string {
	enc := base64Encode(content)
	dir := path.Dir(absPath)
	return strings.Join([]string{
		fmt.Sprintf("sudo install -d -m 0755 %s", shQuote(dir)),
		fmt.Sprintf(`tmp=$(sudo mktemp %s)`, shQuote(dir+"/.inforge-write.XXXXXX")),
		fmt.Sprintf(`printf %%s %s | base64 -d | sudo tee "$tmp" >/dev/null`, shQuote(enc)),
		fmt.Sprintf(`sudo chmod %s "$tmp"`, shQuote(mode)),
		fmt.Sprintf(`sudo chown %s "$tmp"`, shQuote(owner)),
		fmt.Sprintf(`sudo mv "$tmp" %s`, shQuote(absPath)),
	}, "\n")
}
