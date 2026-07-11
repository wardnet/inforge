// Package release implements the R2-backed release artifact store described in
// ADR-0016 (amended for per-architecture artifacts): immutable artifacts keyed
// by SHA and, for services, CPU architecture — <service>/<SHA>-<arch>.tar.gz
// (arch is "amd64" or "arm64", one object per pushed variant) — or, for
// architecture-agnostic app bundles, the legacy unsuffixed
// <service>/<SHA>.tar.gz (pass NoArch). A per-environment manifest
// (<service>/manifest.<env>.yaml) records which SHA (and, for services, which
// detected arch) each host runs. The store is the producer/consumer substrate
// for the `inforge releases` commands — push uploads + prunes, deploy
// downloads + writes the manifest, list reads it.
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

// NewStoreWithAPI is newStoreWithAPI, exported so unit tests in OTHER
// packages (e.g. cmd/inforge) can wire their own minimal fake against a real
// *Store without a live bucket — the same seam this package's own tests use.
// A caller doesn't need to name the unexported s3API type: any value whose
// method set satisfies it (PutObject/GetObject/HeadObject/ListObjectsV2/
// DeleteObject, matching the AWS S3 client's signatures) can be passed here.
func NewStoreWithAPI(api s3API, bucket string) *Store {
	return newStoreWithAPI(api, bucket)
}

// NoArch is the arch to pass for artifacts that are not CPU-architecture
// sensitive (static app bundles). It preserves the legacy
// <service>/<sha>.tar.gz key format exactly — no suffix is appended.
const NoArch = ""

// SupportedArchs is the closed set of CPU architectures inforge builds
// release artifacts for. It is the single source of truth artifactKey's
// suffix and shaFromKey's suffix-stripping both rely on: shaFromKey matches
// against this exact, known set rather than guessing "the text after the last
// dash is an arch" — a --sha value is an unvalidated free-form string (it
// need not be a git commit hash; a caller may pass any tag), so a naive
// last-dash cut would misparse a dash-containing custom SHA (e.g.
// "preview-42") on an unsuffixed (NoArch) key, desyncing it from the
// manifest's pinned SHA and defeating Prune's live-deployment protection.
// cmd/inforge's --arch flag validation reuses this list too, so a pushed
// --arch value and a probed host arch are always directly comparable.
var SupportedArchs = []string{"amd64", "arm64"}

// artifactKey is the object key for a service's (or app's) artifact at a given
// SHA and arch. arch == NoArch keeps the legacy unsuffixed form
// <service>/<sha>.tar.gz; a non-empty arch appends "-<arch>" before the
// extension: <service>/<sha>-<arch>.tar.gz. Two archs of the same SHA are
// therefore two distinct objects, but are treated as ONE logical release
// everywhere that matters — see shaFromKey/ListArtifacts/Prune below.
func artifactKey(service, sha, arch string) string {
	if arch == NoArch {
		return service + "/" + sha + artifactExt
	}
	return service + "/" + sha + "-" + arch + artifactExt
}

// ArtifactKey is artifactKey, exported so callers outside this package (the
// CLI's not-found/pre-flight error messages) can name the exact key they
// looked for without re-deriving the suffix rule.
func ArtifactKey(service, sha, arch string) string { return artifactKey(service, sha, arch) }

// shaFromKey extracts the SHA stem from an artifact key, or "" if key is not an
// artifact of service (e.g. a manifest). It strips a trailing "-<arch>" ONLY
// when arch is exactly one of SupportedArchs — a closed-set match, not a
// "cut at the last dash" guess — so an unsuffixed (app/legacy) key whose SHA
// itself happens to contain a dash (e.g. a custom "preview-42" --sha tag) is
// never misparsed. See SupportedArchs' doc comment for the incident this
// guards against.
func shaFromKey(service, key string) string {
	prefix := service + "/"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, artifactExt) {
		return ""
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(key, prefix), artifactExt)
	if stem == "" || strings.Contains(stem, "/") {
		return ""
	}
	for _, arch := range SupportedArchs {
		if suffix := "-" + arch; strings.HasSuffix(stem, suffix) {
			stem = strings.TrimSuffix(stem, suffix)
			break
		}
	}
	return stem
}

// Artifact is one stored logical release: its SHA and the time R2 last wrote
// it (the recency signal pruning orders on, since SHAs are not time-ordered).
// A SHA pushed under multiple archs coalesces into one Artifact — see
// coalesceArtifacts.
type Artifact struct {
	SHA          string
	LastModified int64 // unix seconds
}

// artifactObject is one raw object under service/ before arch-variant
// coalescing. Prune needs the individual keys (to delete every arch variant of
// a victim SHA); ListArtifacts needs the coalesced view built on top of it.
type artifactObject struct {
	Key          string
	SHA          string
	LastModified int64
}

// PutArtifact uploads body as the artifact for (service, sha, arch),
// overwriting any existing object at that key (re-pushing a SHA+arch is
// idempotent). Pass NoArch for an architecture-agnostic (app) artifact.
func (s *Store) PutArtifact(ctx context.Context, service, sha, arch string, body io.Reader) error {
	key := artifactKey(service, sha, arch)
	_, err := s.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("upload artifact %s: %w", key, err)
	}
	return nil
}

// ArtifactExists reports whether (service, sha, arch) is present in the store.
func (s *Store) ArtifactExists(ctx context.Context, service, sha, arch string) (bool, error) {
	key := artifactKey(service, sha, arch)
	_, err := s.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("head artifact %s: %w", key, err)
	}
	return true, nil
}

// GetArtifact returns a reader for (service, sha, arch); the caller must Close
// it.
func (s *Store) GetArtifact(ctx context.Context, service, sha, arch string) (io.ReadCloser, error) {
	key := artifactKey(service, sha, arch)
	out, err := s.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("artifact %s not found in release store — run `inforge releases push` first", key)
		}
		return nil, fmt.Errorf("download artifact %s: %w", key, err)
	}
	return out.Body, nil
}

// listArtifactObjects pages every artifact object (manifests filtered out via
// shaFromKey returning "") for service, returning the raw per-key rows — one
// per arch variant, before coalescing.
func (s *Store) listArtifactObjects(ctx context.Context, service string) ([]artifactObject, error) {
	var objs []artifactObject
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
			key := aws.ToString(obj.Key)
			sha := shaFromKey(service, key)
			if sha == "" {
				continue
			}
			var lm int64
			if obj.LastModified != nil {
				lm = obj.LastModified.Unix()
			}
			objs = append(objs, artifactObject{Key: key, SHA: sha, LastModified: lm})
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	return objs, nil
}

// coalesceArtifacts collapses every arch variant of a SHA into one Artifact —
// LastModified is the max across variants, so pushing a second arch for an
// already-pushed SHA refreshes that SHA's recency for prune ordering (a
// service isn't "fully released" until every needed arch is pushed). Order is
// first-seen, for deterministic output.
func coalesceArtifacts(objs []artifactObject) []Artifact {
	newest := map[string]int64{}
	var order []string
	for _, o := range objs {
		if _, seen := newest[o.SHA]; !seen {
			order = append(order, o.SHA)
		}
		if o.LastModified > newest[o.SHA] {
			newest[o.SHA] = o.LastModified
		}
	}
	arts := make([]Artifact, 0, len(order))
	for _, sha := range order {
		arts = append(arts, Artifact{SHA: sha, LastModified: newest[sha]})
	}
	return arts
}

// ListArtifacts returns one entry PER LOGICAL RELEASE (SHA) stored for
// service: all arch variants of a SHA collapse into a single Artifact.
func (s *Store) ListArtifacts(ctx context.Context, service string) ([]Artifact, error) {
	objs, err := s.listArtifactObjects(ctx, service)
	if err != nil {
		return nil, err
	}
	return coalesceArtifacts(objs), nil
}

// Prune deletes the oldest unpinned artifacts of service beyond keep, never
// touching a pinned (currently-live) SHA. Victims are selected from the
// coalesced (one-per-SHA) view, but every raw object of a victim SHA is
// deleted — all its arch variants together, never leaving one orphaned. It
// returns the SHAs it deleted. Delete errors are aggregated and returned but
// do not stop the sweep; callers treat them as non-fatal (the upload that
// triggered the prune already succeeded).
func (s *Store) Prune(ctx context.Context, service string, keep int) ([]string, error) {
	pinned, err := s.PinnedSHAs(ctx, service)
	if err != nil {
		return nil, fmt.Errorf("collect pinned SHAs for %s: %w", service, err)
	}
	objs, err := s.listArtifactObjects(ctx, service)
	if err != nil {
		return nil, err
	}
	victims := selectForPrune(coalesceArtifacts(objs), pinned, keep)
	victimSet := make(map[string]bool, len(victims))
	for _, sha := range victims {
		victimSet[sha] = true
	}

	// A victim SHA may have more than one raw key (one per arch variant).
	// variantCount tracks how many it has; deletedCount tracks how many were
	// actually removed. A SHA is only reported in `deleted` once EVERY one of
	// its variants is gone — if e.g. the amd64 key deletes but the arm64 key's
	// DeleteObject fails, that SHA must NOT appear as cleanly pruned: the
	// caller prints `deleted` as an unqualified success line, and a partial
	// deletion silently reported as complete would leave a stray, orphaned
	// object nobody knows to go clean up.
	variantCount := map[string]int{}
	for _, o := range objs {
		if victimSet[o.SHA] {
			variantCount[o.SHA]++
		}
	}

	deletedCount := map[string]int{}
	var errs []error
	for _, o := range objs {
		if !victimSet[o.SHA] {
			continue
		}
		if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(o.Key),
		}); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", o.Key, err))
			continue
		}
		deletedCount[o.SHA]++
	}
	deleted := make([]string, 0, len(victims))
	for _, sha := range victims { // preserve selectForPrune's order
		if deletedCount[sha] == variantCount[sha] {
			deleted = append(deleted, sha)
		}
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
