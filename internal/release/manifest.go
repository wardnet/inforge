package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
	"github.com/wardnet/inforge/internal/yamldoc"
	"gopkg.in/yaml.v3"
)

// manifestCASAttempts bounds the optimistic-concurrency retry loop for a manifest
// update. Contention is rare (a CI concurrency group already serialises the
// common path); this only guards a concurrent direct-from-laptop deploy.
const manifestCASAttempts = 5

// Manifest is the per-environment record of what is live: one entry per host.
// It is the single mutable object per (service, env) and the source of truth
// `releases list` reads and pruning pins against. See ADR-0016.
type Manifest struct {
	Deployments map[string]Deployment `yaml:"deployments"`
}

// Deployment is one host's currently-deployed artifact.
type Deployment struct {
	SHA string `yaml:"sha"`
	// Arch is the detected CPU architecture of the host this SHA was
	// delivered to (amd64/arm64), recorded for operator visibility in
	// `releases list`. It is informational only — never read back to decide
	// what to fetch; the live SSH probe at each deploy is the source of
	// truth, since a host's underlying instance could change between
	// deploys. Empty for app deployments (architecture-agnostic) and for any
	// manifest entry written before arch-awareness existed.
	Arch       string    `yaml:"arch,omitempty"`
	DeployedAt time.Time `yaml:"deployedAt"`
}

// SHAs returns the set of SHAs the manifest pins (one per host, deduplicated).
func (m Manifest) SHAs() map[string]bool {
	set := make(map[string]bool, len(m.Deployments))
	for _, d := range m.Deployments {
		if d.SHA != "" {
			set[d.SHA] = true
		}
	}
	return set
}

func manifestKey(service, env string) string {
	return service + "/manifest." + env + ".yaml"
}

func isManifestKey(service, key string) bool {
	return strings.HasPrefix(key, service+"/manifest.") && strings.HasSuffix(key, ".yaml")
}

// LoadManifest fetches the (service, env) manifest. It returns the parsed
// manifest, the object's ETag (for a conditional write), and whether it exists.
// A missing manifest is not an error — it yields an empty manifest, no ETag,
// exists=false.
func (s *Store) LoadManifest(ctx context.Context, service, env string) (Manifest, string, bool, error) {
	out, err := s.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(manifestKey(service, env)),
	})
	if err != nil {
		if isNotFound(err) {
			return Manifest{Deployments: map[string]Deployment{}}, "", false, nil
		}
		return Manifest{}, "", false, fmt.Errorf("read manifest %s/%s: %w", service, env, err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return Manifest{}, "", false, fmt.Errorf("read manifest body %s/%s: %w", service, env, err)
	}
	doc, err := yamldoc.Parse(fmt.Sprintf("%s/%s manifest", service, env), data)
	if err != nil {
		return Manifest{}, "", false, err
	}
	var m Manifest
	if err := doc.Decode(&m); err != nil {
		return Manifest{}, "", false, fmt.Errorf("parse release manifest: %w", err)
	}
	if m.Deployments == nil {
		m.Deployments = map[string]Deployment{}
	}
	return m, aws.ToString(out.ETag), true, nil
}

// SetDeployment records that host now runs sha (deployed at now) in the
// (service, env) manifest, using an If-Match compare-and-swap so a concurrent
// writer cannot silently clobber a different host's entry. On a 412 it refetches
// and retries, bounded by manifestCASAttempts.
func (s *Store) SetDeployment(ctx context.Context, service, env, host, sha, arch string, now time.Time) error {
	for attempt := 0; attempt < manifestCASAttempts; attempt++ {
		m, etag, exists, err := s.LoadManifest(ctx, service, env)
		if err != nil {
			return err
		}
		m.Deployments[host] = Deployment{SHA: sha, Arch: arch, DeployedAt: now.UTC()}
		body, err := yaml.Marshal(m)
		if err != nil {
			return fmt.Errorf("marshal manifest %s/%s: %w", service, env, err)
		}
		in := &s3.PutObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(manifestKey(service, env)),
			Body:   strings.NewReader(string(body)),
		}
		// Conditional write: require the object to be unchanged since we read it
		// (If-Match), or — when we believe it does not exist yet — require that it
		// still does not (If-None-Match: *). Either failing means a concurrent
		// writer raced us, so we refetch and retry.
		if exists {
			in.IfMatch = aws.String(etag)
		} else {
			in.IfNoneMatch = aws.String("*")
		}
		if _, err := s.s3.PutObject(ctx, in); err != nil {
			if isPreconditionFailed(err) {
				continue
			}
			return fmt.Errorf("write manifest %s/%s: %w", service, env, err)
		}
		return nil
	}
	return fmt.Errorf("write manifest %s/%s: gave up after %d concurrent-update retries", service, env, manifestCASAttempts)
}

// PinnedSHAs is the union of SHAs across every environment manifest for service:
// because artifacts are env-agnostic (one <service>/<SHA>.tar.gz serves all
// envs), a SHA live in any environment must be exempt from pruning.
func (s *Store) PinnedSHAs(ctx context.Context, service string) (map[string]bool, error) {
	pinned := map[string]bool{}
	var token *string
	for {
		out, err := s.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(service + "/"),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list manifests for %s: %w", service, err)
		}
		for _, obj := range out.Contents {
			key := aws.ToString(obj.Key)
			if !isManifestKey(service, key) {
				continue
			}
			m, err := s.getManifestByKey(ctx, key)
			if err != nil {
				return nil, err
			}
			for sha := range m.SHAs() {
				pinned[sha] = true
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	return pinned, nil
}

// DeleteEnvManifests removes every workload's manifest for env
// (<workload>/manifest.<env>.yaml) across the whole bucket, returning the keys it
// deleted. It exists for ephemeral teardown (ADR-0028): PinnedSHAs unions the SHAs
// of every manifest under a workload's prefix, so a torn-down env's manifest left
// behind would pin its SHAs against pruning forever. Only manifests are matched —
// artifacts are env-agnostic and are never deleted here.
func (s *Store) DeleteEnvManifests(ctx context.Context, env string) ([]string, error) {
	if env == "" {
		return nil, errors.New("delete manifests: empty env")
	}
	suffix := "/manifest." + env + ".yaml"
	var keys []string
	var token *string
	for {
		out, err := s.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list manifests for env %s: %w", env, err)
		}
		for _, obj := range out.Contents {
			if key := aws.ToString(obj.Key); strings.HasSuffix(key, suffix) {
				keys = append(keys, key)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	sort.Strings(keys)
	deleted := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		}); err != nil {
			return deleted, fmt.Errorf("delete manifest %s: %w", key, err)
		}
		deleted = append(deleted, key)
	}
	return deleted, nil
}

func (s *Store) getManifestByKey(ctx context.Context, key string) (Manifest, error) {
	out, err := s.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest body %s: %w", key, err)
	}
	doc, err := yamldoc.Parse(key, data)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := doc.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("parse release manifest: %w", err)
	}
	return m, nil
}

// isPreconditionFailed reports whether err is a 412 from a conditional write.
func isPreconditionFailed(err error) bool {
	if err == nil {
		return false
	}
	if httpStatus(err) == 412 {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "PreconditionFailed"
	}
	return false
}
