// Package release implements the R2-backed release artifact store described in
// ADR-0016: immutable, SHA-keyed artifacts at <service>/<SHA>.tar.gz and a
// per-environment manifest (<service>/manifest.<env>.yaml) recording which SHA
// each host runs. The store is the producer/consumer substrate for the
// `inforge releases` commands — push uploads + prunes, deploy downloads + writes
// the manifest, list reads it.
package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
	"github.com/wardnet/inforge/internal/r2"
)

// artifactExt is the suffix every artifact object carries; the SHA is the key
// stem. Listing filters on it so manifests (and any future siblings) are never
// mistaken for artifacts.
const artifactExt = ".tar.gz"

// s3API is the subset of the S3 client the store uses. A fake implements it in
// tests so prune and manifest-CAS logic run without a live bucket.
type s3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// Store is a release artifact store backed by one R2 (S3-compatible) bucket.
type Store struct {
	s3     s3API
	bucket string
}

// NewStore builds a Store against Cloudflare R2's S3-compatible API for bucket,
// using accountID to form the endpoint and the standard AWS credential chain
// (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) for auth. The R2 client wiring
// (endpoint, path-style, checksum settings) is shared with the backups store
// via internal/r2.
func NewStore(ctx context.Context, bucket, accountID string) (*Store, error) {
	if bucket == "" {
		return nil, errors.New("release store: empty bucket")
	}
	client, err := r2.NewClient(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("release store: %w", err)
	}
	return &Store{s3: client, bucket: bucket}, nil
}

// newStoreWithAPI is the test seam: it wraps an arbitrary s3API.
func newStoreWithAPI(api s3API, bucket string) *Store {
	return &Store{s3: api, bucket: bucket}
}

// artifactKey is the object key for a service's artifact at a given SHA.
func artifactKey(service, sha string) string {
	return service + "/" + sha + artifactExt
}

// shaFromKey extracts the SHA stem from an artifact key, or "" if key is not an
// artifact of service (e.g. a manifest).
func shaFromKey(service, key string) string {
	prefix := service + "/"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, artifactExt) {
		return ""
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(key, prefix), artifactExt)
	if stem == "" || strings.Contains(stem, "/") {
		return ""
	}
	return stem
}

// Artifact is one stored artifact: its SHA and the time R2 last wrote it (the
// recency signal pruning orders on, since SHAs are not time-ordered).
type Artifact struct {
	SHA          string
	LastModified int64 // unix seconds
}

// PutArtifact uploads body as the artifact for (service, sha), overwriting any
// existing object at that key (re-pushing a SHA is idempotent).
func (s *Store) PutArtifact(ctx context.Context, service, sha string, body io.Reader) error {
	_, err := s.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(artifactKey(service, sha)),
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("upload artifact %s/%s: %w", service, sha, err)
	}
	return nil
}

// ArtifactExists reports whether (service, sha) is present in the store.
func (s *Store) ArtifactExists(ctx context.Context, service, sha string) (bool, error) {
	_, err := s.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(artifactKey(service, sha)),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("head artifact %s/%s: %w", service, sha, err)
	}
	return true, nil
}

// GetArtifact returns a reader for (service, sha); the caller must Close it.
func (s *Store) GetArtifact(ctx context.Context, service, sha string) (io.ReadCloser, error) {
	out, err := s.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(artifactKey(service, sha)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("artifact %s/%s not found in release store — run `inforge releases push` first", service, sha)
		}
		return nil, fmt.Errorf("download artifact %s/%s: %w", service, sha, err)
	}
	return out.Body, nil
}

// ListArtifacts returns every artifact stored for service.
func (s *Store) ListArtifacts(ctx context.Context, service string) ([]Artifact, error) {
	var arts []Artifact
	var token *string
	for {
		out, err := s.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(service + "/"),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list artifacts for %s: %w", service, err)
		}
		for _, obj := range out.Contents {
			sha := shaFromKey(service, aws.ToString(obj.Key))
			if sha == "" {
				continue
			}
			var lm int64
			if obj.LastModified != nil {
				lm = obj.LastModified.Unix()
			}
			arts = append(arts, Artifact{SHA: sha, LastModified: lm})
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	return arts, nil
}

// Prune deletes the oldest unpinned artifacts of service beyond keep, never
// touching a pinned (currently-live) SHA. It returns the SHAs it deleted. Delete
// errors are aggregated and returned but do not stop the sweep; callers treat
// them as non-fatal (the upload that triggered the prune already succeeded).
func (s *Store) Prune(ctx context.Context, service string, keep int) ([]string, error) {
	pinned, err := s.PinnedSHAs(ctx, service)
	if err != nil {
		return nil, fmt.Errorf("collect pinned SHAs for %s: %w", service, err)
	}
	arts, err := s.ListArtifacts(ctx, service)
	if err != nil {
		return nil, err
	}
	victims := selectForPrune(arts, pinned, keep)

	var deleted []string
	var errs []error
	for _, sha := range victims {
		if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(artifactKey(service, sha)),
		}); err != nil {
			errs = append(errs, fmt.Errorf("delete %s/%s: %w", service, sha, err))
			continue
		}
		deleted = append(deleted, sha)
	}
	return deleted, errors.Join(errs...)
}

// selectForPrune is the pure retention policy: given every artifact, the set of
// pinned SHAs, and how many unpinned (historical) artifacts to keep, it returns
// the SHAs to delete — the oldest unpinned ones beyond keep. Pinned SHAs are
// never returned and do not count toward keep. keep <= 0 disables pruning (a
// missing/zero `keep` must never trigger deletion), so it returns nil.
func selectForPrune(arts []Artifact, pinned map[string]bool, keep int) []string {
	if keep <= 0 {
		return nil
	}
	unpinned := make([]Artifact, 0, len(arts))
	for _, a := range arts {
		if !pinned[a.SHA] {
			unpinned = append(unpinned, a)
		}
	}
	if len(unpinned) <= keep {
		return nil
	}
	// Newest first; everything past the first `keep` is surplus history.
	sort.Slice(unpinned, func(i, j int) bool {
		if unpinned[i].LastModified != unpinned[j].LastModified {
			return unpinned[i].LastModified > unpinned[j].LastModified
		}
		return unpinned[i].SHA < unpinned[j].SHA // stable tiebreak
	})
	victims := make([]string, 0, len(unpinned)-keep)
	for _, a := range unpinned[keep:] {
		victims = append(victims, a.SHA)
	}
	return victims
}

// isNotFound reports whether err is an S3 "object does not exist" error, across
// the modeled NoSuchKey/NotFound types and a bare 404.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if httpStatus(err) == 404 {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}

// httpStatus extracts the HTTP status code from an SDK error if present (the
// smithy transport error and the test fake both expose HTTPStatusCode), or 0.
func httpStatus(err error) int {
	var se interface{ HTTPStatusCode() int }
	if errors.As(err, &se) {
		return se.HTTPStatusCode()
	}
	return 0
}
