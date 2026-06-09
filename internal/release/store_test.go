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
	assert.Equal(t, "abc123", shaFromKey("bridge", "bridge/abc123.tar.gz"))
	assert.Equal(t, "", shaFromKey("bridge", "bridge/manifest.prd.yaml"), "manifest is not an artifact")
	assert.Equal(t, "", shaFromKey("bridge", "other/abc123.tar.gz"), "wrong service prefix")
	assert.Equal(t, "", shaFromKey("bridge", "bridge/sub/abc.tar.gz"), "nested key is not an artifact")
	assert.Equal(t, "", shaFromKey("bridge", "bridge/.tar.gz"), "empty stem")
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

	exists, err := s.ArtifactExists(ctx, "bridge", "sha1")
	require.NoError(t, err)
	assert.False(t, exists)

	_, err = s.GetArtifact(ctx, "bridge", "sha1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run `inforge releases push` first")

	require.NoError(t, s.PutArtifact(ctx, "bridge", "sha1", strings.NewReader("payload-bytes")))

	exists, err = s.ArtifactExists(ctx, "bridge", "sha1")
	require.NoError(t, err)
	assert.True(t, exists)

	rc, err := s.GetArtifact(ctx, "bridge", "sha1")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	assert.Equal(t, "payload-bytes", string(got))
}

// TestPruneExemptsPinnedAcrossEnvs is the end-to-end retention check: a SHA live
// in any env manifest survives a prune even when it is the oldest artifact.
func TestPruneExemptsPinnedAcrossEnvs(t *testing.T) {
	ctx := context.Background()
	fake := newFakeS3()
	s := newStoreWithAPI(fake, "artifacts")

	// Upload oldest→newest so LastModified order is shaA < shaB < shaC < shaD.
	for _, sha := range []string{"shaA", "shaB", "shaC", "shaD"} {
		require.NoError(t, s.PutArtifact(ctx, "bridge", sha, strings.NewReader(sha)))
	}
	// shaA is live in prd (oldest) and shaB is live in qa — both must be exempt.
	require.NoError(t, s.SetDeployment(ctx, "bridge", "prd", "bridge-01", "shaA", testNow()))
	require.NoError(t, s.SetDeployment(ctx, "bridge", "qa", "bridge-01", "shaB", testNow()))

	// keep=1 unpinned history. Unpinned = {shaC, shaD}; newest (shaD) kept, shaC deleted.
	deleted, err := s.Prune(ctx, "bridge", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"shaC"}, deleted)

	for _, sha := range []string{"shaA", "shaB", "shaD"} {
		ok, _ := s.ArtifactExists(ctx, "bridge", sha)
		assert.True(t, ok, "%s should survive prune", sha)
	}
	ok, _ := s.ArtifactExists(ctx, "bridge", "shaC")
	assert.False(t, ok, "shaC should be pruned")
}
