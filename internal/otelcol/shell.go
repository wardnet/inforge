package otelcol

import (
	"encoding/base64"
	"path"
	"strings"
)

// shQuote single-quotes s for safe interpolation into a POSIX shell command,
// escaping any embedded single quote as the standard '\'' sequence. The package
// renders host shell but stays Pulumi-free (it is pure, like internal/nginx), so it
// carries its own minimal quoting rather than importing the remote helpers.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// base64Encode returns the standard base64 of s, for transporting arbitrary bytes
// (including a secret) through a shell command without exposing them in argv.
func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// parentDir is the directory containing absPath.
func parentDir(absPath string) string {
	return path.Dir(absPath)
}
