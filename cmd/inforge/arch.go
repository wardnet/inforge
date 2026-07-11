package main

import (
	"fmt"
	"slices"

	"github.com/wardnet/inforge/internal/release"
)

// supportedArchs re-exports release.SupportedArchs (the actual single source
// of truth, since it's what artifactKey/shaFromKey encode) under a
// package-local name for readability at call sites in this package.
var supportedArchs = release.SupportedArchs

// validArch reports whether arch is one of supportedArchs.
func validArch(arch string) bool {
	return slices.Contains(supportedArchs, arch)
}

// validateArchFlag returns a clear error if arch is not a supported CPU
// architecture.
func validateArchFlag(arch string) error {
	if !validArch(arch) {
		return fmt.Errorf("invalid --arch %q: must be amd64 or arm64", arch)
	}
	return nil
}

// mapUnameArch maps a target host's `uname -m` output to the Go/goreleaser
// architecture name inforge uses everywhere else (release artifact keys,
// --arch flags): the same x86_64->amd64 / aarch64->arm64 table
// agentDownloadStep already bakes into the on-host provisioning shell
// (program/program.go), reproduced here in Go so it can be probed and
// compared at deploy time instead of only ever running remotely.
func mapUnameArch(raw string) (string, error) {
	switch raw {
	case "x86_64":
		return "amd64", nil
	case "aarch64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported host architecture %q", raw)
	}
}
