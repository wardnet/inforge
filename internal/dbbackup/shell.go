package dbbackup

import (
	"encoding/base64"
	"path"
	"strings"
)

// shQuote single-quotes s for safe interpolation into a POSIX shell command,
// escaping any embedded single quote as the standard '\'' sequence. The package
// renders host shell but stays Pulumi-free (pure, like internal/postgres/otelcol),
// so it carries its own minimal quoting rather than importing the remote helpers.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// base64Encode returns the standard base64 of s, for transporting arbitrary bytes
// (a rendered unit, a credential) through a shell command without exposing them in
// argv.
func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// parentDir is the directory containing absPath.
func parentDir(absPath string) string {
	return path.Dir(absPath)
}

// writeFileScript renders sudo shell that writes content to absPath with the given
// mode and owner, creating the parent dir. content is base64-encoded for transport
// so arbitrary bytes (incl. a secret) survive intact and never appear in argv as
// plaintext; the decode is piped straight into a root-owned install. Mirrors
// otelcol.WriteFileScript / postgres.writeFileScript.
func writeFileScript(absPath, content, mode, owner string) string {
	enc := base64Encode(content)
	dir := parentDir(absPath)
	// Stage to a temp then atomic mv: `install /dev/stdin <existing-file>` fails on
	// coreutils 9 (Ubuntu 26.04) — "install: No such file or directory" — when the
	// target already exists (the R2 credential on a re-deploy). The mv also makes the
	// write atomic; the temp is 0600 by mktemp and moded before the mv.
	lines := []string{
		"sudo install -d -m 0755 " + shQuote(dir),
		"tmp=$(sudo mktemp " + shQuote(dir+"/.inforge-write.XXXXXX") + ")",
		`printf %s ` + shQuote(enc) + ` | base64 -d | sudo tee "$tmp" >/dev/null`,
		"sudo chmod " + shQuote(mode) + ` "$tmp"`,
	}
	if owner != "" {
		lines = append(lines, "sudo chown "+shQuote(owner)+` "$tmp"`)
	}
	lines = append(lines, "sudo mv \"$tmp\" "+shQuote(absPath))
	return strings.Join(lines, "\n")
}
