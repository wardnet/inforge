// Package backupstore is the CLI-side reader over the per-database Postgres
// backup bucket (ADR-0036). Backups are written on the cluster host
// (`pg_dump -Fc | gzip` → R2) and pruned on the host; this package only *lists*
// the archives for `inforge db list-backups` and for `inforge db restore` to
// resolve the newest key. It is deliberately separate from internal/release
// (the release-artifact store): a different bucket, a different key scheme
// (`<env>/<region>/<cluster>/<database>/<ts>.dump.gz`, no SHA/manifest), and a
// different retention model (on-host, not CLI-driven). The two share only the
// R2 client wiring via internal/r2.
package backupstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/wardnet/inforge/internal/r2"
)

// s3API is the subset of the S3 client the store uses. A fake implements it in
// tests so listing runs without a live bucket.
type s3API interface {
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// Store lists backup objects in one R2 (S3-compatible) bucket.
type Store struct {
	s3     s3API
	bucket string
}

// NewStore builds a Store against Cloudflare R2's S3-compatible API for bucket,
// using accountID to form the endpoint and the standard AWS credential chain.
// The R2 client wiring is shared with the release store via internal/r2.
func NewStore(ctx context.Context, bucket, accountID string) (*Store, error) {
	if bucket == "" {
		return nil, errors.New("backup store: empty bucket")
	}
	client, err := r2.NewClient(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("backup store: %w", err)
	}
	return &Store{s3: client, bucket: bucket}, nil
}

// newStoreWithAPI is the test seam: it wraps an arbitrary s3API.
func newStoreWithAPI(api s3API, bucket string) *Store {
	return &Store{s3: api, bucket: bucket}
}

// Object is one stored backup archive.
type Object struct {
	Key          string
	Size         int64
	LastModified int64 // unix seconds
}

// List returns every object under prefix, sorted by key. Because the backup key
// ends in an ISO-8601 UTC timestamp, key order is chronological — so the last
// element is the newest backup (what `inforge db restore` defaults to).
func (s *Store) List(ctx context.Context, prefix string) ([]Object, error) {
	var objs []Object
	var token *string
	for {
		out, err := s.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list backups under %q: %w", prefix, err)
		}
		for _, obj := range out.Contents {
			var size, lm int64
			if obj.Size != nil {
				size = *obj.Size
			}
			if obj.LastModified != nil {
				lm = obj.LastModified.Unix()
			}
			objs = append(objs, Object{Key: aws.ToString(obj.Key), Size: size, LastModified: lm})
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	// No sort: ListObjectsV2 returns keys in UTF-8 lexicographic order and preserves
	// it across continuation pages, so appending page-by-page already yields a
	// globally ordered slice. Because a backup key ends in a fixed-width ISO-8601 UTC
	// timestamp, that lexical order is chronological — so the last element is the
	// newest backup (what `inforge db restore` defaults to).
	return objs, nil
}
