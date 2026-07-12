package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSlug(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		slug, err := generateSlug()
		require.NoError(t, err)
		// Every generated slug carries the prefix and is a valid DNS-safe identity.
		assert.True(t, len(slug) > len(ephemeralSlugPrefix), "slug %q too short", slug)
		assert.Equal(t, ephemeralSlugPrefix, slug[:len(ephemeralSlugPrefix)])
		require.NoError(t, validateSlug(slug), "generated slug %q must be DNS-safe", slug)
		seen[slug] = true
	}
	// 200 draws from 36^4 should not collide in practice; allow a tiny margin but
	// catch a constant/low-entropy generator (which would collapse to a few values).
	assert.Greater(t, len(seen), 190, "generated slugs are not unique enough — entropy source suspect")
}

func TestValidateSlug(t *testing.T) {
	valid := []string{"eph-7f3k", "abc", "a1", "my-preview-1", "eph-0000"}
	for _, s := range valid {
		assert.NoError(t, validateSlug(s), "expected %q valid", s)
	}
	invalid := []string{
		"",         // empty
		"Eph-7f3k", // uppercase
		"-eph",     // leading hyphen
		"eph-",     // trailing hyphen
		"eph_7f3k", // underscore
		"eph.7f3k", // dot
		"this-slug-name-is-way-too-long-to-be-valid", // > 24 chars
	}
	for _, s := range invalid {
		assert.Error(t, validateSlug(s), "expected %q invalid", s)
	}
}

func TestResolveTTL(t *testing.T) {
	max := 24 * time.Hour

	got, err := resolveTTL("", max)
	require.NoError(t, err)
	assert.Equal(t, defaultEphemeralTTL, got, "empty --ttl defaults to 2h")

	got, err = resolveTTL("90m", max)
	require.NoError(t, err)
	assert.Equal(t, 90*time.Minute, got)

	got, err = resolveTTL("24h", max)
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, got, "TTL exactly at the cap is allowed")

	// Over the cap is rejected.
	_, err = resolveTTL("48h", max)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds the maximum")

	// Below the minimum floor is rejected (an env must not be born expired).
	_, err = resolveTTL("1m", max)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "below the minimum")
	got, err = resolveTTL(minEphemeralTTL.String(), max)
	require.NoError(t, err)
	assert.Equal(t, minEphemeralTTL, got, "TTL exactly at the floor is allowed")

	// Zero / negative / unparseable are rejected.
	_, err = resolveTTL("0s", max)
	require.Error(t, err)
	_, err = resolveTTL("-1h", max)
	require.Error(t, err)
	_, err = resolveTTL("banana", max)
	require.Error(t, err)
}

func TestEphemeralMaxTTLDefault(t *testing.T) {
	d, err := ephemeralConfig{}.maxTTL()
	require.NoError(t, err)
	assert.Equal(t, defaultEphemeralMaxTTL, d)

	d, err = ephemeralConfig{MaxTTL: "12h"}.maxTTL()
	require.NoError(t, err)
	assert.Equal(t, 12*time.Hour, d)

	_, err = ephemeralConfig{MaxTTL: "nope"}.maxTTL()
	require.Error(t, err)

	// A ceiling below the floor would make every --ttl unsatisfiable, so it is
	// rejected here (at the knob) rather than surfacing as a confusing `up` error.
	_, err = ephemeralConfig{MaxTTL: "5m"}.maxTTL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "below the minimum")
}

func TestRequireObjectBackend(t *testing.T) {
	for _, typ := range []string{"r2", "s3"} {
		assert.NoError(t, requireObjectBackend(projectConfig{Backend: backendConfig{Type: typ}}), "backend %q should be allowed", typ)
	}
	for _, typ := range []string{"git-branch", "file", ""} {
		err := requireObjectBackend(projectConfig{Backend: backendConfig{Type: typ}})
		require.Error(t, err, "backend %q should be rejected", typ)
		assert.Contains(t, err.Error(), "object-store state backend")
	}
}

func TestReapDecision(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	past := "999000"    // before now
	future := "1001000" // after now

	// Ephemeral + expired → reap, no fail-safe reason.
	exp, reap, reason := reapDecision("true", past, now)
	assert.True(t, reap)
	assert.Empty(t, reason)
	assert.Equal(t, time.Unix(999000, 0), exp)

	// Ephemeral + not yet expired → no reap.
	_, reap, _ = reapDecision("true", future, now)
	assert.False(t, reap)

	// Not ephemeral, even if its expires_at is in the past → NEVER reaped (the
	// signal that protects every permanent env).
	_, reap, _ = reapDecision("", past, now)
	assert.False(t, reap)
	_, reap, _ = reapDecision("false", past, now)
	assert.False(t, reap)

	// Ephemeral but missing / malformed expires_at → reaped FAIL-SAFE (bounds
	// billing on an unambiguously disposable env) with a reason, never skip-forever.
	_, reap, reason = reapDecision("true", "", now)
	assert.True(t, reap)
	assert.NotEmpty(t, reason)
	_, reap, reason = reapDecision("true", "not-a-number", now)
	assert.True(t, reap)
	assert.NotEmpty(t, reason)
}

func TestExpiresAtRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	raw := expiresAtEpoch(now, 2*time.Hour)
	got, err := parseExpiresAt(raw)
	require.NoError(t, err)
	assert.Equal(t, now.Add(2*time.Hour).Unix(), got.Unix())
}

func TestSourceHostDNS(t *testing.T) {
	// Regional: <compute>.vm.<env>.<slug>.<base> — only the env label (index 2)
	// flips back to the source env.
	assert.Equal(t,
		"bridge.vm.testing.use1.wardnet.network",
		sourceHostDNS("bridge.vm.eph-7f3k.use1.wardnet.network", "eph-7f3k", "testing"))
	// Global: <compute>.vm.<env>.<base> — env still at index 2.
	assert.Equal(t,
		"web.vm.testing.wardnet.network",
		sourceHostDNS("web.vm.eph-7f3k.wardnet.network", "eph-7f3k", "testing"))
	// No-op when the env label does not match the identity env (defensive).
	assert.Equal(t,
		"bridge.vm.prd.use1.wardnet.network",
		sourceHostDNS("bridge.vm.prd.use1.wardnet.network", "eph-7f3k", "testing"))
}

func TestCheckSlugFree(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o750))

	// A slug naming a defined env would stamp the ephemeral identity (and the
	// reaper's expires_at) onto that permanent env's stack.
	err := checkSlugFree(dir, "prd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "names a defined env")

	assert.NoError(t, checkSlugFree(dir, "eph-7f3k"))
}

// testProjectConfig binds a project to a throwaway file:// Pulumi backend and
// chdirs into a scratch workdir (the inline-source workspace writes Pulumi.yaml
// into the cwd). The Automation API shells out to the pulumi CLI, so the test is
// skipped where it is not installed.
func testProjectConfig(t *testing.T) projectConfig {
	t.Helper()
	if _, err := exec.LookPath("pulumi"); err != nil {
		t.Skip("pulumi CLI not installed")
	}
	state := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv("PULUMI_CONFIG_PASSPHRASE", "test")
	return projectConfig{Name: "inforgetest", Backend: backendConfig{Type: "file", URL: "file://" + state}}
}

// TestSelectStackDoesNotCreate: the teardown paths must never mint a stack. A
// typo'd slug has to error out, leaving the name free for a later `up` (an
// upserted empty stack would be unreapable AND unusable — createStack refuses an
// existing name forever).
func TestSelectStackDoesNotCreate(t *testing.T) {
	ctx := context.Background()
	projCfg := testProjectConfig(t)

	_, err := selectStack(ctx, "eph-typo", projCfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no stack named")

	// The slug is still free: a fresh `up` can claim it.
	_, err = createStack(ctx, "eph-typo", projCfg)
	require.NoError(t, err)

	// And now that it exists, select finds it.
	_, err = selectStack(ctx, "eph-typo", projCfg)
	require.NoError(t, err)
}

// TestEphemeralPostUpUsesStackConfigDeployKey: in the standard CI setup the SSH
// deploy key lives ONLY in the source stack config (deploy_private_key), which
// `up` copies onto the ephemeral stack. The post-provision SSH work must resolve
// it from there — resolving via the env-only path would hard-fail every `up`
// after the whole env had been provisioned.
func TestEphemeralPostUpUsesStackConfigDeployKey(t *testing.T) {
	ctx := context.Background()
	projCfg := testProjectConfig(t)
	t.Setenv("INFORGE_DEPLOY_KEY", "")
	t.Setenv("INFORGE_DEPLOY_PRIVATE_KEY", "")

	s, err := createStack(ctx, "eph-key1", projCfg)
	require.NoError(t, err)
	require.NoError(t, s.SetConfig(ctx, "deploy_private_key", auto.ConfigValue{Value: "KEYMATERIAL", Secret: true}))

	// An empty resources tree has no mesh hosts and no workloads, so the run has
	// nothing to push: it succeeds iff the key resolved from the stack config.
	dir := t.TempDir()
	require.NoError(t, ephemeralPostUp(ctx, s, projCfg, dir, "prd", "eph-key1", ""))

	// Control: with the stack-config key gone there is no key anywhere, and the
	// same run refuses before touching a host.
	require.NoError(t, s.RemoveConfig(ctx, "deploy_private_key"))
	err = ephemeralPostUp(ctx, s, projCfg, dir, "prd", "eph-key1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSH deploy key required")
}
