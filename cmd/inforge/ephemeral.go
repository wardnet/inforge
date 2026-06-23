package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

// The ephemeral command group (ADR-0028) clones a source env's resource
// definition into a fresh, network-segregated env under a generated slug
// identity, deploys the exact service/app SHAs currently live in the source, and
// guarantees teardown via a TTL reaped out-of-band:
//
//	inforge ephemeral up   --from <src> [--slug <s>] [--ttl <dur>]
//	inforge ephemeral down <slug>
//	inforge ephemeral reap [--dry-run]
//
// Identity is decoupled from config source: the slug is the env identity (every
// name, FQDN, and label), while the stack config's source_environment points at
// the resources/<src>/ tree the deploy reads. See ADR-0028.

const (
	// defaultEphemeralTTL is the lifetime applied when `up` gets no --ttl.
	defaultEphemeralTTL = 2 * time.Hour
	// minEphemeralTTL floors --ttl so an env is not born (near-)expired. `up`
	// stamps expires_at BEFORE the provision + replicate run — deliberately, so a
	// crashed `up` still leaves a reap-able stack — so a TTL near the build time
	// would let the next `reap` destroy the env mid-build. This floor is a
	// MITIGATION, not a guarantee: it must comfortably exceed a cold multi-region
	// provision (image pulls, cloud-init, ACME issuance, per-host scp/ssh
	// replicate). 30m clears a realistic worst case; a consumer provisioning
	// something larger should pass an explicit, longer --ttl.
	minEphemeralTTL = 30 * time.Minute
	// ephemeralSlugPrefix prefixes every auto-generated slug, so an ephemeral
	// env is recognisable by name (though the reaper classifies by stack config,
	// never by name — see ephemeral_reap.go).
	ephemeralSlugPrefix = "eph-"
	// slugRandLen is the number of random base-36 characters appended after the
	// prefix. 4 chars = 36^4 ≈ 1.7M combinations — ample for concurrent envs.
	slugRandLen = 4
)

// Stack-config keys the ephemeral commands persist (the single carrier of the
// source mapping — down/reap read source_environment back from here). They mirror
// the keys program.Run reads in its Run entry point.
const (
	cfgKeyEnvironment       = "environment"
	cfgKeySourceEnvironment = "source_environment"
	cfgKeyEphemeral         = "ephemeral"
	cfgKeyExpiresAt         = "expires_at"
)

// slugPattern is a DNS-label-safe slug: lowercase alphanumerics and hyphens,
// starting and ending alphanumeric. It is baked into every cloud resource name
// (wardnet-<slug>-…), so it must be a valid DNS label fragment.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func newEphemeralCmd(configPath, dir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ephemeral",
		Aliases: []string{"eph"},
		Short:   "Spin up, tear down, and reap ephemeral (preview) environments",
		Long: "Clone a source environment's definition into a fresh, network-segregated\n" +
			"env under a generated slug identity, running the exact service/app SHAs live\n" +
			"in the source, with a TTL reaped out-of-band. Requires an r2/s3 state backend.",
	}
	cmd.AddCommand(
		newEphemeralUpCmd(configPath, dir),
		newEphemeralDownCmd(configPath),
		newEphemeralReapCmd(configPath),
	)
	return cmd
}

// generateSlug returns a fresh DNS-safe ephemeral slug ("eph-<4 base36>"). It
// uses crypto/rand (Math.random()-style collisions across concurrent CI runs
// would alias two envs onto one stack), failing closed if the entropy source is
// unavailable rather than emitting a predictable slug.
func generateSlug() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, slugRandLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate slug: %w", err)
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return ephemeralSlugPrefix + string(buf), nil
}

// validateSlug rejects a user-supplied --slug that is not a valid DNS label
// fragment (it becomes part of every cloud resource name and FQDN). It also
// bounds the length so the composed wardnet-<env>-<region>-<type>-<name> stays
// within DNS/cloud limits.
func validateSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("slug is empty")
	}
	if len(slug) > 24 {
		return fmt.Errorf("slug %q is too long (max 24 chars)", slug)
	}
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("slug %q is not DNS-safe (use lowercase letters, digits, and hyphens; start and end alphanumeric)", slug)
	}
	return nil
}

// resolveTTL parses the optional --ttl (default 2h) and enforces the project's
// hard ceiling (inforge.yaml ephemeral.maxTtl, default 24h). A zero/negative TTL
// or one over the ceiling is rejected — an ephemeral env that never expires
// would defeat the reaper.
func resolveTTL(flag string, maxTTL time.Duration) (time.Duration, error) {
	ttl := defaultEphemeralTTL
	if flag != "" {
		d, err := time.ParseDuration(flag)
		if err != nil {
			return 0, fmt.Errorf("parse --ttl %q: %w", flag, err)
		}
		ttl = d
	}
	if ttl < minEphemeralTTL {
		return 0, fmt.Errorf("--ttl %s is below the minimum %s — `up` stamps expires_at before the multi-minute provision, so a shorter TTL risks the env being reaped mid-build", ttl, minEphemeralTTL)
	}
	if ttl > maxTTL {
		return 0, fmt.Errorf("--ttl %s exceeds the maximum %s (raise ephemeral.maxTtl in inforge.yaml to allow longer)", ttl, maxTTL)
	}
	return ttl, nil
}

// expiresAtEpoch renders a TTL deadline as epoch seconds (a decimal string).
// Hetzner label values forbid ':' so an RFC3339 timestamp can't be a label; epoch
// seconds are label-safe and the reaper compares them numerically.
func expiresAtEpoch(now time.Time, ttl time.Duration) string {
	return strconv.FormatInt(now.Add(ttl).Unix(), 10)
}

// parseExpiresAt parses the epoch-seconds expires_at written by `up`. A malformed
// value is reported so the reaper can flag (rather than silently skip) a stack
// whose deadline it cannot read.
func parseExpiresAt(raw string) (time.Time, error) {
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse expires_at %q: %w", raw, err)
	}
	return time.Unix(secs, 0), nil
}

// checkSourceDefined enforces decision #1 (ADR-0028): the source env must be
// DEFINED in the checkout — its resources/<src>/ tree must be present — because
// `up` (and a later cold `down`/`reap`) re-runs the inline program over that tree
// to resolve the resource graph. The source need not be currently deployed;
// nothing here reads its live cloud state.
func checkSourceDefined(dir, srcEnv string) error {
	envDir := filepath.Join(dir, srcEnv)
	info, err := os.Stat(envDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source env %q is not defined: %s does not exist (the ephemeral clone re-runs the program over the source's resource tree, which must be in the checkout)", srcEnv, envDir)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source env %q: %s is not a directory", srcEnv, envDir)
	}
	return nil
}
