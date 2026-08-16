package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/internal/yamldoc"
)

// defaultEphemeralMaxTTL is the built-in hard ceiling on an ephemeral env's TTL,
// applied when inforge.yaml's ephemeral.maxTtl is unset (ADR-0028).
const defaultEphemeralMaxTTL = 24 * time.Hour

type backendConfig struct {
	// Type is one of: file, git-branch, s3, r2.
	// Defaults to "file" when unset.
	Type string `yaml:"type"`
	// URL is the backend URL for "file" and "s3" types (e.g. file://.pulumi,
	// s3://my-bucket/prefix). For "r2" the URL is r2://<bucket-name>.
	URL string `yaml:"url"`
	// Branch is the git branch used for state storage when Type is "git-branch".
	Branch string `yaml:"branch"`
}

type projectConfig struct {
	Name    string        `yaml:"name"`
	Backend backendConfig `yaml:"backend"`
	// Artifacts configures the release artifact store (ADR-0016). Optional —
	// only `inforge releases` reads it.
	Artifacts artifactsConfig `yaml:"artifacts"`
	// Providers carries project-level provider defaults applied when a resource
	// spec omits its provider field.
	Providers types.ProviderDefaults `yaml:"providers"`
	// Ephemeral configures the `inforge ephemeral` command group (ADR-0028).
	// Optional — only the ephemeral commands read it.
	Ephemeral ephemeralConfig `yaml:"ephemeral"`
	// Backups configures the self-hosted Postgres backup destination (ADR-0036).
	// Optional — only cluster hosts with a backup-enabled database read it.
	Backups backupsConfig `yaml:"backups"`
}

// backupsConfig is the inforge.yaml `backups:` block: the dedicated R2 bucket
// per-database Postgres dumps are uploaded to (ADR-0036). It is authored flat as a
// bucket NAME (not a backend URL); the R2 endpoint is derived from
// CLOUDFLARE_ACCOUNT_ID, exactly like the artifacts store. The bucket must be
// distinct from both the state and the artifacts buckets (validated at load) — it
// holds sensitive full data with its own lifecycle, so it never colocates with
// state or release tarballs.
type backupsConfig struct {
	Bucket string `yaml:"bucket"`
}

// configured reports whether a backups bucket is declared.
func (b backupsConfig) configured() bool { return b.Bucket != "" }

// resolve returns the backup destination bucket and its R2 S3 endpoint URL. The
// endpoint is derived from CLOUDFLARE_ACCOUNT_ID like the artifacts/state R2 stores.
func (b backupsConfig) resolve() (bucket, endpoint string, err error) {
	if b.Bucket == "" {
		return "", "", fmt.Errorf("backups.bucket is not configured")
	}
	endpoint, err = r2Endpoint()
	if err != nil {
		return "", "", err
	}
	return b.Bucket, endpoint, nil
}

// ephemeralConfig is the inforge.yaml `ephemeral:` block (ADR-0028).
type ephemeralConfig struct {
	// MaxTTL is the hard ceiling on an ephemeral env's TTL (a Go duration string,
	// e.g. "24h"). A larger --ttl is rejected. Empty defaults to 24h.
	MaxTTL string `yaml:"maxTtl"`
}

// maxTTL returns the configured TTL ceiling, defaulting to 24h when unset.
func (e ephemeralConfig) maxTTL() (time.Duration, error) {
	if e.MaxTTL == "" {
		return defaultEphemeralMaxTTL, nil
	}
	d, err := time.ParseDuration(e.MaxTTL)
	if err != nil {
		return 0, fmt.Errorf("ephemeral.maxTtl %q: %w", e.MaxTTL, err)
	}
	// The ceiling must leave the [minEphemeralTTL, maxTtl] window non-empty —
	// otherwise resolveTTL rejects every --ttl (the default and any explicit value)
	// with a confusing per-invocation error. Catch the misconfiguration here, at
	// the knob, rather than letting `up` fail unsatisfiably.
	if d < minEphemeralTTL {
		return 0, fmt.Errorf("ephemeral.maxTtl %q is below the minimum ephemeral TTL %s — no --ttl could ever satisfy both bounds", e.MaxTTL, minEphemeralTTL)
	}
	return d, nil
}

// artifactsConfig is the inforge.yaml `artifacts:` block: the release-store
// bucket and how many historical (unpinned) artifacts to retain per service.
type artifactsConfig struct {
	// Backend points at the release-artifact bucket. It MUST be a different
	// bucket from the state Backend (validated at load).
	Backend backendConfig `yaml:"backend"`
	// Keep is the number of unpinned (rollback-history) artifacts retained per
	// service after a push prunes. 0 or unset disables pruning; SHAs live in any
	// environment's manifest are always kept and never count toward Keep.
	Keep int `yaml:"keep"`
}

// configured reports whether an artifacts backend is declared.
func (a artifactsConfig) configured() bool {
	return a.Backend.Type != "" || a.Backend.URL != ""
}

// bucket returns the bucket name for an r2:// or s3:// backend URL.
func (b backendConfig) bucket() (string, error) {
	switch b.Type {
	case "r2":
		return r2Bucket(b.URL)
	case "s3":
		u, err := url.Parse(b.URL)
		if err != nil {
			return "", fmt.Errorf("parse s3 URL: %w", err)
		}
		return u.Host, nil
	default:
		return "", fmt.Errorf("backend type %q has no bucket (valid: r2, s3)", b.Type)
	}
}

// backendURL returns the Pulumi-compatible backend URL for the project config.
// It translates r2:// to the appropriate s3:// URL with the Cloudflare R2
// S3-compatible endpoint. The Cloudflare account ID is read from
// CLOUDFLARE_ACCOUNT_ID.
//
// It is also the single choke point every Automation API entry point passes
// through (upsertStack, createStack, selectStack, ephemeralWorkspace), which is
// why the S3 checksum opt-out is asserted here: the setting has to be in the
// environment before the `pulumi` CLI subprocess is spawned, and doing it in one
// place means a new entry point cannot forget it.
func (c projectConfig) backendURL() (string, error) {
	switch c.Backend.Type {
	case "", "file", "git-branch":
		return c.Backend.URL, nil
	case "s3":
		// A plain s3:// URL carrying an explicit endpoint= is a third-party store
		// too (MinIO, IBM COS), not AWS — same checksum problem as R2.
		if strings.Contains(c.Backend.URL, "endpoint=") {
			if err := ensureS3ChecksumCompat(); err != nil {
				return "", err
			}
		}
		return c.Backend.URL, nil
	case "r2":
		if err := ensureS3ChecksumCompat(); err != nil {
			return "", err
		}
		return r2ToS3URL(c.Backend.URL)
	default:
		return "", fmt.Errorf("unsupported backend type %q (valid: file, git-branch, s3, r2)", c.Backend.Type)
	}
}

// ensureS3ChecksumCompat opts the AWS SDK out of computing request checksums it
// adds by default, for S3-compatible object stores that are not AWS S3.
//
// The v2 AWS SDK computes a CRC32 checksum with aws-chunked streaming on every
// PutObject. Cloudflare R2 rejects those uploads — the state write fails with
// `InvalidDigest: The checksum or Content-MD5 you specified is not valid` (400)
// and no checkpoint is ever committed.
//
// Pulumi 3.256.0 started defaulting this to when_required itself when the s3://
// backend URL sets a custom endpoint, but that landed as a fix for a DIFFERENT
// symptom (403s on IBM COS/MinIO) and did not spare us — prd broke on 3.256.0
// with the 400 above while 3.253.0 was fine. Rather than depend on the CLI's
// detection matching our URL shape, we assert the setting ourselves: Pulumi
// explicitly honours AWS_REQUEST_CHECKSUM_CALCULATION as the escape hatch and
// leaves it alone when already set, so this behaves the same on every CLI
// version and on a local run outside CI.
//
// An operator who sets the variable deliberately wins — we only fill a blank.
func ensureS3ChecksumCompat() error {
	if os.Getenv(awsRequestChecksumEnv) != "" {
		return nil
	}
	if err := os.Setenv(awsRequestChecksumEnv, "when_required"); err != nil {
		return fmt.Errorf("set %s: %w", awsRequestChecksumEnv, err)
	}
	return nil
}

// awsRequestChecksumEnv is the AWS SDK setting controlling whether request
// checksums are computed. Named once so the guard and the override agree.
const awsRequestChecksumEnv = "AWS_REQUEST_CHECKSUM_CALCULATION"

// r2Bucket extracts the bucket name from an r2://<bucket> URL.
func r2Bucket(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse r2 URL: %w", err)
	}
	if u.Scheme != "r2" {
		return "", fmt.Errorf("r2 URL must start with r2://")
	}
	bucket := strings.TrimPrefix(u.Path, "/")
	if u.Host != "" {
		bucket = u.Host
	}
	return bucket, nil
}

// r2Endpoint returns the Cloudflare R2 S3-compatible endpoint URL for the account
// named by CLOUDFLARE_ACCOUNT_ID, validating the account id is lowercase hex. It is
// shared by the state/artifacts backend translation and the backups destination.
func r2Endpoint() (string, error) {
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	if accountID == "" {
		return "", fmt.Errorf("r2 backend requires CLOUDFLARE_ACCOUNT_ID environment variable")
	}
	for _, ch := range accountID {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return "", fmt.Errorf("CLOUDFLARE_ACCOUNT_ID must be a lowercase hex string, got %q", accountID)
		}
	}
	return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID), nil
}

// r2ToS3URL translates an r2://<bucket> URL to the Pulumi s3:// URL format
// for Cloudflare R2's S3-compatible API.
func r2ToS3URL(raw string) (string, error) {
	bucket, err := r2Bucket(raw)
	if err != nil {
		return "", err
	}
	endpoint, err := r2Endpoint()
	if err != nil {
		return "", err
	}
	// region=auto is R2's pseudo-region; the AWS SDK requires a region even
	// though R2 does not use one.
	return fmt.Sprintf("s3://%s?region=auto&endpoint=%s&s3ForcePathStyle=true", bucket, url.QueryEscape(endpoint)), nil
}

type stackConfig struct {
	Config map[string]string `yaml:"config"`
}

func loadProjectConfig(path string) (projectConfig, error) {
	var cfg projectConfig
	data, err := os.ReadFile(path) // #nosec G304 -- path is the operator-supplied --config CLI flag, not attacker input
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("inforge.yaml not found — run from the repo root or pass --config")
		}
		return cfg, err
	}
	doc, err := yamldoc.Parse(path, data)
	if err != nil {
		return cfg, err
	}
	if err := doc.Decode(&cfg); err != nil {
		return cfg, err
	}
	if cfg.Name == "" {
		return cfg, fmt.Errorf("%s: name is required", path)
	}
	if err := cfg.validateBuckets(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// validateBuckets enforces bucket separation across the three object stores inforge
// keys into R2 (ADR-0016 extended to a third store in ADR-0036): the Pulumi state
// bucket, the release-artifact bucket, and the Postgres backups bucket must be
// mutually distinct. Colocating them risks one store's retention/lifecycle sweep
// touching another's objects — and backups hold sensitive full data with their own
// lifecycle. A bucket name is only known for r2/s3 backends; file/git-branch state
// trivially differs, so it is compared only when it has a bucket.
func (c projectConfig) validateBuckets() error {
	// stateBucket is "" for file/git-branch backends (no object bucket to collide).
	stateBucket := ""
	if c.Backend.Type == "r2" || c.Backend.Type == "s3" {
		b, err := c.Backend.bucket()
		if err != nil {
			return fmt.Errorf("backend: %w", err)
		}
		stateBucket = b
	}

	artBucket := ""
	if c.Artifacts.configured() {
		b, err := c.Artifacts.Backend.bucket()
		if err != nil {
			return fmt.Errorf("artifacts.backend: %w", err)
		}
		artBucket = b
		if stateBucket != "" && artBucket == stateBucket {
			return fmt.Errorf("artifacts.backend bucket %q must differ from the state backend bucket", artBucket)
		}
	}

	if c.Backups.configured() {
		bakBucket := c.Backups.Bucket
		// backups.bucket is a BARE bucket name (the R2 endpoint is derived), unlike the
		// state/artifacts backends which take an r2://<bucket> URL. Reject a URL/path here
		// so a copied `r2://…` value fails at load rather than producing a broken
		// `s3://r2://…` upload target that only fails on each host at timer time.
		if strings.ContainsAny(bakBucket, ":/") {
			return fmt.Errorf("backups.bucket %q must be a bare bucket name, not a URL or path (the R2 endpoint is derived from CLOUDFLARE_ACCOUNT_ID)", bakBucket)
		}
		if stateBucket != "" && bakBucket == stateBucket {
			return fmt.Errorf("backups.bucket %q must differ from the state backend bucket", bakBucket)
		}
		if artBucket != "" && bakBucket == artBucket {
			return fmt.Errorf("backups.bucket %q must differ from the artifacts backend bucket", bakBucket)
		}
	}
	return nil
}

func loadStackConfig(path string) (stackConfig, error) {
	var cfg stackConfig
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-supplied --stack-config flag or a locally derived default filename
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("stack config not found: %s", path)
		}
		return cfg, err
	}
	doc, err := yamldoc.Parse(path, data)
	if err != nil {
		return cfg, err
	}
	if err := doc.Decode(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// resolveStackConfig loads the optional per-stack config for a command.
//
// The environment is derived from the stack name (see program/program.go), so the
// stack config file is no longer required just to name the env — it only carries
// optional `config:` key/value pairs pushed onto the Pulumi stack. So a *defaulted*
// path (inforge.<stack>.yaml) that does not exist is not an error: we return empty
// config, and applyStackConfig no-ops on it. A path the caller asked for explicitly
// via --stack-config is still loaded strictly — a missing file there is a real
// mistake, not an intended "no config".
func resolveStackConfig(explicitPath, stackName string) (stackConfig, error) {
	if explicitPath != "" {
		return loadStackConfig(explicitPath)
	}
	path := "inforge." + stackName + ".yaml"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return stackConfig{}, nil
	}
	return loadStackConfig(path)
}
