package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLockStore is an in-memory stackLockChecker.
type fakeLockStore struct {
	keys    map[string]time.Time
	deleted []string
}

func (f *fakeLockStore) ListObjectsV2(_ context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	out := &s3.ListObjectsV2Output{}
	for k, ts := range f.keys {
		if strings.HasPrefix(k, aws.ToString(params.Prefix)) {
			mod := ts
			out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k), LastModified: &mod})
		}
	}
	return out, nil
}

func (f *fakeLockStore) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	key := aws.ToString(params.Key)
	delete(f.keys, key)
	f.deleted = append(f.deleted, key)
	return &s3.DeleteObjectOutput{}, nil
}

// A lock belongs to a stack only on an exact path-SEGMENT match: stack "prd"
// must not match "prd-old" or a project named "prd-infra", and another stack's
// active lock must never block this stack's deploy.
func TestFindStackLocksMatchesBySegment(t *testing.T) {
	store := &fakeLockStore{keys: map[string]time.Time{
		".pulumi/locks/organization/wardnet-infrastructure/prd/aaaa.json":     time.Now(),
		".pulumi/locks/organization/wardnet-infrastructure/prd-old/bbbb.json": time.Now(),
		".pulumi/locks/organization/wardnet-infrastructure/eph-x1y2/cc.json":  time.Now(),
		".pulumi/meta.yaml": time.Now(),
	}}
	locks, err := findStackLocks(context.Background(), store, "bucket", "prd")
	require.NoError(t, err)
	require.Len(t, locks, 1)
	assert.Contains(t, locks[0].Key, "/prd/")
}

// unlockStackLocks deletes exactly the stack's locks and reports them; a
// lock-free stack is a no-op, not an error (the common "was it actually
// locked?" probe).
func TestUnlockStackLocksDeletesOnlyTheStacksLocks(t *testing.T) {
	store := &fakeLockStore{keys: map[string]time.Time{
		".pulumi/locks/organization/proj/prd/aaaa.json": time.Now(),
		".pulumi/locks/organization/proj/stage/bb.json": time.Now(),
		".pulumi/locks/organization/proj/prd/cccc.json": time.Now(),
		".pulumi/stacks/proj/prd.json":                  time.Now(), // a checkpoint must NEVER be deleted
	}}
	var out bytes.Buffer
	require.NoError(t, unlockStackLocks(context.Background(), store, "bucket", "prd", &out))
	assert.ElementsMatch(t, []string{
		".pulumi/locks/organization/proj/prd/aaaa.json",
		".pulumi/locks/organization/proj/prd/cccc.json",
	}, store.deleted)
	assert.Contains(t, out.String(), "2 lock(s) removed")
	// The other stack's lock and the checkpoint survive.
	assert.Contains(t, store.keys, ".pulumi/locks/organization/proj/stage/bb.json")
	assert.Contains(t, store.keys, ".pulumi/stacks/proj/prd.json")

	// Lock-free: a clean no-op with a clear message.
	out.Reset()
	require.NoError(t, unlockStackLocks(context.Background(), store, "bucket", "ghost", &out))
	assert.Contains(t, out.String(), "holds no locks")
	assert.Len(t, store.deleted, 2, "nothing further deleted")
}

// The fail-fast error must carry the lock identity, its age, and the remedy —
// it replaces a 30-minute silent hang, so it has to explain itself completely.
func TestLockedStackError(t *testing.T) {
	assert.NoError(t, lockedStackError(nil, "prd"), "no locks — no error")
	err := lockedStackError([]lockObject{{Key: ".pulumi/locks/organization/proj/prd/aaaa.json", Modified: time.Now().Add(-30 * time.Minute)}}, "prd")
	require.Error(t, err)
	for _, want := range []string{
		"is locked by a previous update",
		".pulumi/locks/organization/proj/prd/aaaa.json",
		"30m0s ago",
		"inforge stack unlock prd",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

// stateLockClient: r2 backends get a client + bucket; other backends skip the
// check (ok=false) rather than guessing; a malformed r2 URL fails loudly.
func TestStateLockClient(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "abc123def456")
	_, bucket, ok, err := stateLockClient(context.Background(), projectConfig{Backend: backendConfig{Type: "r2", URL: "r2://state-bucket"}})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "state-bucket", bucket)

	_, _, ok, err = stateLockClient(context.Background(), projectConfig{Backend: backendConfig{Type: "git-branch"}})
	require.NoError(t, err)
	assert.False(t, ok, "git-branch has no shared lock objects")

	_, _, _, err = stateLockClient(context.Background(), projectConfig{Backend: backendConfig{Type: "r2", URL: "r2://bad/path"}})
	require.Error(t, err, "a malformed r2 URL must fail loudly")
}

// The thin shells: command construction and the backend-gated early returns.
func TestStackCommandSurface(t *testing.T) {
	cfg := "inforge.yaml"
	cmd := newStackCmd(&cfg)
	assert.Equal(t, "stack", cmd.Use)
	sub, _, err := cmd.Find([]string{"unlock"})
	require.NoError(t, err)
	assert.Contains(t, sub.Use, "unlock")
}

func TestFailOnStackLockSkipsLocklessBackends(t *testing.T) {
	// file/git-branch backends have no shared lock objects: the check is a no-op.
	assert.NoError(t, failOnStackLock(context.Background(), projectConfig{Backend: backendConfig{Type: "git-branch"}}, "prd"))
	// A broken backend config fails loudly rather than deploying blind.
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	assert.Error(t, failOnStackLock(context.Background(), projectConfig{Backend: backendConfig{Type: "r2", URL: "r2://b"}}, "prd"))
}

func TestRunStackUnlockRejectsLocklessBackends(t *testing.T) {
	err := runStackUnlock(context.Background(), projectConfig{Backend: backendConfig{Type: "git-branch"}}, "prd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no shared lock objects")
}
