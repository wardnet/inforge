package release

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShaFromKey(t *testing.T) {
	assert.Equal(t, "abc123", shaFromKey("bridge", "bridge/abc123.tar.gz"), "unsuffixed (app/legacy) key")
	assert.Equal(t, "abc123", shaFromKey("bridge", "bridge/abc123-amd64.tar.gz"), "amd64 arch suffix stripped")
	assert.Equal(t, "abc123", shaFromKey("bridge", "bridge/abc123-arm64.tar.gz"), "arm64 arch suffix stripped")
	assert.Equal(t, "", shaFromKey("bridge", "bridge/manifest.prd.yaml"), "manifest is not an artifact")
	assert.Equal(t, "", shaFromKey("bridge", "other/abc123.tar.gz"), "wrong service prefix")
	assert.Equal(t, "", shaFromKey("bridge", "bridge/sub/abc.tar.gz"), "nested key is not an artifact")
	assert.Equal(t, "", shaFromKey("bridge", "bridge/.tar.gz"), "empty stem")
}

func TestArtifactKey(t *testing.T) {
	assert.Equal(t, "bridge/abc.tar.gz", artifactKey("bridge", "abc", NoArch), "apps/legacy: no suffix")
	assert.Equal(t, "bridge/abc-amd64.tar.gz", artifactKey("bridge", "abc", "amd64"))
	assert.Equal(t, "bridge/abc-arm64.tar.gz", artifactKey("bridge", "abc", "arm64"))
	assert.Equal(t, artifactKey("bridge", "abc", "amd64"), ArtifactKey("bridge", "abc", "amd64"), "exported wrapper matches")
}

func TestSelectForPrune(t *testing.T) {
	arts := []Artifact{
		{SHA: "old1", LastModified: 10},
		{SHA: "old2", LastModified: 20},
		{SHA: "mid", LastModified: 30},
		{SHA: "new", LastModified: 40},
		{SHA: "live", LastModified: 5}, // oldest, but pinned
	}
	pinned := map[string]bool{"live": true}

	t.Run("keep zero disables pruning", func(t *testing.T) {
		assert.Nil(t, selectForPrune(arts, pinned, 0))
	})

	t.Run("pinned is never deleted and does not count toward keep", func(t *testing.T) {
		// 4 unpinned, keep 2 → delete the 2 oldest unpinned (old1, old2). live is
		// the oldest overall but pinned, so it survives.
		got := selectForPrune(arts, pinned, 2)
		assert.ElementsMatch(t, []string{"old1", "old2"}, got)
	})

	t.Run("keep >= unpinned count deletes nothing", func(t *testing.T) {
		assert.Nil(t, selectForPrune(arts, pinned, 4))
	})

	t.Run("all pinned deletes nothing", func(t *testing.T) {
		allPinned := map[string]bool{"old1": true, "old2": true, "mid": true, "new": true, "live": true}
		assert.Nil(t, selectForPrune(arts, allPinned, 1))
	})
}

func TestPutGetExistsArtifact(t *testing.T) {
	ctx := context.Background()
	s := newStoreWithAPI(newFakeS3(), "artifacts")

	// Apps / architecture-agnostic path (NoArch): unsuffixed key.
	exists, err := s.ArtifactExists(ctx, "bridge", "sha1", NoArch)
	require.NoError(t, err)
	assert.False(t, exists)

	_, err = s.GetArtifact(ctx, "bridge", "sha1", NoArch)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bridge/sha1.tar.gz not found")
	assert.Contains(t, err.Error(), "run `inforge releases push` first")

	require.NoError(t, s.PutArtifact(ctx, "bridge", "sha1", NoArch, strings.NewReader("payload-bytes")))

	exists, err = s.ArtifactExists(ctx, "bridge", "sha1", NoArch)
	require.NoError(t, err)
	assert.True(t, exists)

	rc, err := s.GetArtifact(ctx, "bridge", "sha1", NoArch)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	assert.Equal(t, "payload-bytes", string(got))

	// Services: real arch, suffixed key, error message names the exact key.
	require.NoError(t, s.PutArtifact(ctx, "bridge", "sha2", "amd64", strings.NewReader("amd64-bytes")))
	exists, err = s.ArtifactExists(ctx, "bridge", "sha2", "amd64")
	require.NoError(t, err)
	assert.True(t, exists)
	exists, err = s.ArtifactExists(ctx, "bridge", "sha2", "arm64")
	require.NoError(t, err)
	assert.False(t, exists, "the amd64 push must not satisfy an arm64 lookup")

	_, err = s.GetArtifact(ctx, "bridge", "sha2", "arm64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bridge/sha2-arm64.tar.gz not found")
}

// TestListArtifactsCoalescesArchVariants asserts two arch variants of the same
// SHA collapse into ONE logical release, with LastModified = the max across
// variants (a service isn't "fully released" until every needed arch lands).
func TestListArtifactsCoalescesArchVariants(t *testing.T) {
	ctx := context.Background()
	fake := newFakeS3()
	s := newStoreWithAPI(fake, "artifacts")

	require.NoError(t, s.PutArtifact(ctx, "bridge", "shaC", "amd64", strings.NewReader("amd64")))
	require.NoError(t, s.PutArtifact(ctx, "bridge", "shaC", "arm64", strings.NewReader("arm64")))

	arts, err := s.ListArtifacts(ctx, "bridge")
	require.NoError(t, err)
	require.Len(t, arts, 1, "both arch variants of shaC coalesce into one Artifact")
	assert.Equal(t, "shaC", arts[0].SHA)
}

// TestPruneExemptsPinnedAcrossEnvs is the end-to-end retention check: a SHA live
// in any env manifest survives a prune even when it is the oldest artifact.
func TestPruneExemptsPinnedAcrossEnvs(t *testing.T) {
	ctx := context.Background()
	fake := newFakeS3()
	s := newStoreWithAPI(fake, "artifacts")

	// Upload oldest→newest so LastModified order is shaA < shaB < shaC < shaD.
	for _, sha := range []string{"shaA", "shaB", "shaC", "shaD"} {
		require.NoError(t, s.PutArtifact(ctx, "bridge", sha, NoArch, strings.NewReader(sha)))
	}
	// shaA is live in prd (oldest) and shaB is live in qa — both must be exempt.
	require.NoError(t, s.SetDeployment(ctx, "bridge", "prd", "bridge-01", "shaA", "amd64", testNow()))
	require.NoError(t, s.SetDeployment(ctx, "bridge", "qa", "bridge-01", "shaB", "amd64", testNow()))

	// keep=1 unpinned history. Unpinned = {shaC, shaD}; newest (shaD) kept, shaC deleted.
	deleted, err := s.Prune(ctx, "bridge", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"shaC"}, deleted)

	for _, sha := range []string{"shaA", "shaB", "shaD"} {
		ok, _ := s.ArtifactExists(ctx, "bridge", sha, NoArch)
		assert.True(t, ok, "%s should survive prune", sha)
	}
	ok, _ := s.ArtifactExists(ctx, "bridge", "shaC", NoArch)
	assert.False(t, ok, "shaC should be pruned")
}

// TestPruneDeletesEveryArchVariant asserts a victim SHA pushed under both archs
// has BOTH keys deleted together — never leaving one arch variant orphaned.
func TestPruneDeletesEveryArchVariant(t *testing.T) {
	ctx := context.Background()
	fake := newFakeS3()
	s := newStoreWithAPI(fake, "artifacts")

	require.NoError(t, s.PutArtifact(ctx, "bridge", "shaOld", "amd64", strings.NewReader("old-amd64")))
	require.NoError(t, s.PutArtifact(ctx, "bridge", "shaOld", "arm64", strings.NewReader("old-arm64")))
	require.NoError(t, s.PutArtifact(ctx, "bridge", "shaNew", "amd64", strings.NewReader("new-amd64")))

	deleted, err := s.Prune(ctx, "bridge", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"shaOld"}, deleted)

	amd64Exists, _ := s.ArtifactExists(ctx, "bridge", "shaOld", "amd64")
	arm64Exists, _ := s.ArtifactExists(ctx, "bridge", "shaOld", "arm64")
	assert.False(t, amd64Exists, "amd64 variant must be pruned")
	assert.False(t, arm64Exists, "arm64 variant must be pruned too — never left orphaned")

	newExists, _ := s.ArtifactExists(ctx, "bridge", "shaNew", "amd64")
	assert.True(t, newExists, "shaNew is within the keep window")
}
