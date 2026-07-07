package postgres

import (
	"encoding/base64"
	"path"
	"strings"
)

// shQuote single-quotes s for safe interpolation into a POSIX shell command,
// escaping any embedded single quote as the standard '\'' sequence. The package
// renders host shell but stays Pulumi-free (pure, like internal/nginx/internal/otelcol),
// so it carries its own minimal quoting rather than importing the remote helpers.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// base64Encode returns the standard base64 of s, for transporting arbitrary bytes
// (a rendered config, a statement carrying a generated password) through a shell
// command without exposing them in argv.
func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// parentDir is the directory containing absPath.
func parentDir(absPath string) string {
	return path.Dir(absPath)
}

// writeFileScript renders sudo shell that writes content to absPath with the given
// mode and owner, creating the parent dir. content is base64-encoded for transport so
// arbitrary bytes survive intact and never appear in argv as plaintext; the decode is
// piped straight into a root-owned install. Mirrors otelcol.WriteFileScript.
func writeFileScript(absPath, content, mode, owner string) string {
	enc := base64Encode(content)
	dir := parentDir(absPath)
	lines := []string{
		// `mkdir -p` (NOT `install -d -m 0755`): postgres config files live inside
		// PGDATA, which initdb created 0700. `install -d -m 0755` re-modes an existing
		// dir, flipping PGDATA to 0755 → the postmaster then refuses to start
		// ("data directory … has invalid permissions"). mkdir -p only creates missing
		// dirs and never touches an existing dir's mode.
		"sudo mkdir -p " + shQuote(dir),
		"printf %s " + shQuote(enc) + " | base64 -d | sudo install -m " + shQuote(mode) + " /dev/stdin " + shQuote(absPath),
	}
	if owner != "" {
		lines = append(lines, "sudo chown "+shQuote(owner)+" "+shQuote(absPath))
	}
	return strings.Join(lines, "\n")
}
