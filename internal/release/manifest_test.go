package release

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func testNow() time.Time { return time.Unix(1_700_000_500, 0).UTC() }

func TestSetDeploymentCreatesAndMerges(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithAPI(newFakeS3(), "artifacts")

	// First write creates the manifest (no prior object → If-None-Match path).
	require.NoError(t, s.SetDeployment(ctx, "bridge", "prd", "bridge-01", "sha1", "amd64", testNow()))
	// Second write for a different host must preserve the first entry.
	require.NoError(t, s.SetDeployment(ctx, "bridge", "prd", "bridge-02", "sha2", "amd64", testNow()))
	// Re-deploying an existing host updates it in place.
	require.NoError(t, s.SetDeployment(ctx, "bridge", "prd", "bridge-01", "sha3", "amd64", testNow()))

	m, _, exists, err := s.LoadManifest(ctx, "bridge", "prd")
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, "sha3", m.Deployments["bridge-01"].SHA)
	assert.Equal(t, "sha2", m.Deployments["bridge-02"].SHA)
	assert.Len(t, m.Deployments, 2)
}

// TestSetDeploymentRetriesOnConcurrentWrite injects a concurrent manifest update
// between our read and conditional write: the first PutObject sees a stale ETag
// (412) and the CAS loop must refetch and retry, ending with BOTH writers' host
// entries intact rather than one clobbering the other.
func TestSetDeploymentRetriesOnConcurrentWrite(t *testing.T) {
	ctx := context.Background()
	fake := newFakeS3()
	s := newStoreWithAPI(fake, "artifacts")

	// Seed an existing manifest so the contended write uses If-Match.
	require.NoError(t, s.SetDeployment(ctx, "bridge", "prd", "bridge-01", "sha1", "amd64", testNow()))

	var fired bool
	fake.putHook = func(key string) {
		if fired || !strings.HasSuffix(key, "manifest.prd.yaml") {
			return
		}
		fired = true // one-shot; the racing write below re-enters but is gated out
		// A concurrent deployer records host bridge-03 first, bumping the ETag.
		require.NoError(t, s.SetDeployment(ctx, "bridge", "prd", "bridge-03", "sha9", "amd64", testNow()))
	}

	// Our write for bridge-02 should hit 412 once, refetch, and succeed.
	require.NoError(t, s.SetDeployment(ctx, "bridge", "prd", "bridge-02", "sha2", "amd64", testNow()))

	m, _, _, err := s.LoadManifest(ctx, "bridge", "prd")
	require.NoError(t, err)
	assert.Equal(t, "sha1", m.Deployments["bridge-01"].SHA)
	assert.Equal(t, "sha2", m.Deployments["bridge-02"].SHA, "our entry must survive the race")
	assert.Equal(t, "sha9", m.Deployments["bridge-03"].SHA, "concurrent entry must not be clobbered")
}

// TestDeploymentArchOmitEmpty asserts Arch round-trips through yaml and that a
// zero-value (app deployments; pre-arch-awareness history) Arch is omitted
// from the marshaled output rather than serialized as an empty "arch:" key.
func TestDeploymentArchOmitEmpty(t *testing.T) {
	withArch, err := yaml.Marshal(Deployment{SHA: "sha1", Arch: "amd64", DeployedAt: testNow()})
	require.NoError(t, err)
	assert.Contains(t, string(withArch), "arch: amd64")

	var roundTripped Deployment
	require.NoError(t, yaml.Unmarshal(withArch, &roundTripped))
	assert.Equal(t, "amd64", roundTripped.Arch)

	noArch, err := yaml.Marshal(Deployment{SHA: "sha1", DeployedAt: testNow()})
	require.NoError(t, err)
	assert.NotContains(t, string(noArch), "arch:")
}

func TestPinnedSHAsUnionAcrossEnvs(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithAPI(newFakeS3(), "artifacts")

	require.NoError(t, s.SetDeployment(ctx, "bridge", "prd", "bridge-01", "shaP", "amd64", testNow()))
	require.NoError(t, s.SetDeployment(ctx, "bridge", "qa", "bridge-01", "shaQ", "amd64", testNow()))
	require.NoError(t, s.SetDeployment(ctx, "bridge", "qa", "bridge-02", "shaP", "amd64", testNow())) // dup across envs

	pinned, err := s.PinnedSHAs(ctx, "bridge")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"shaP": true, "shaQ": true}, pinned)
}
