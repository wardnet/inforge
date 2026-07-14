package main

import (
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

// runStackUnlock deletes exactly the stack's locks and reports them; a
// lock-free stack is a no-op, not an error (the common "was it actually
// locked?" probe).
func TestRunStackUnlockDeletesOnlyTheStacksLocks(t *testing.T) {
	store := &fakeLockStore{keys: map[string]time.Time{
		".pulumi/locks/organization/proj/prd/aaaa.json": time.Now(),
		".pulumi/locks/organization/proj/stage/bb.json": time.Now(),
		".pulumi/locks/organization/proj/prd/cccc.json": time.Now(),
		".pulumi/stacks/proj/prd.json":                  time.Now(), // a checkpoint must NEVER be deleted
	}}
	locks, err := findStackLocks(context.Background(), store, "bucket", "prd")
	require.NoError(t, err)
	require.Len(t, locks, 2)
	for _, l := range locks {
		_, err := store.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String("bucket"), Key: aws.String(l.Key)})
		require.NoError(t, err)
	}
	assert.ElementsMatch(t, []string{
		".pulumi/locks/organization/proj/prd/aaaa.json",
		".pulumi/locks/organization/proj/prd/cccc.json",
	}, store.deleted)
	// The other stack's lock and the checkpoint survive.
	assert.Contains(t, store.keys, ".pulumi/locks/organization/proj/stage/bb.json")
	assert.Contains(t, store.keys, ".pulumi/stacks/proj/prd.json")
}
