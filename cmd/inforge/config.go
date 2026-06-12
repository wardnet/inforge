package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

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
func (c projectConfig) backendURL() (string, error) {
	switch c.Backend.Type {
	case "", "file", "s3", "git-branch":
		return c.Backend.URL, nil
	case "r2":
		return r2ToS3URL(c.Backend.URL)
	default:
		return "", fmt.Errorf("unsupported backend type %q (valid: file, git-branch, s3, r2)", c.Backend.Type)
	}
}

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

// r2ToS3URL translates an r2://<bucket> URL to the Pulumi s3:// URL format
// for Cloudflare R2's S3-compatible API.
func r2ToS3URL(raw string) (string, error) {
	bucket, err := r2Bucket(raw)
	if err != nil {
		return "", err
	}
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	if accountID == "" {
		return "", fmt.Errorf("r2 backend requires CLOUDFLARE_ACCOUNT_ID environment variable")
	}
	for _, ch := range accountID {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return "", fmt.Errorf("CLOUDFLARE_ACCOUNT_ID must be a lowercase hex string, got %q", accountID)
		}
	}
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
	// region=auto is R2's pseudo-region; the AWS SDK requires a region even
	// though R2 does not use one.
	return fmt.Sprintf("s3://%s?region=auto&endpoint=%s&s3ForcePathStyle=true", bucket, url.QueryEscape(endpoint)), nil
}

type stackConfig struct {
	Config map[string]string `yaml:"config"`
}

func loadProjectConfig(path string) (projectConfig, error) {
	var cfg projectConfig
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("inforge.yaml not found — run from the repo root or pass --config")
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Name == "" {
		return cfg, fmt.Errorf("%s: name is required", path)
	}
	if err := cfg.validateArtifacts(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// validateArtifacts enforces that the release-artifact bucket is distinct from
// the state bucket — colocating release tarballs with Pulumi state risks a
// retention sweep touching state objects. A bucket name can only be compared for
// r2/s3 backends; other state backend types (file, git-branch) trivially differ.
func (c projectConfig) validateArtifacts() error {
	if !c.Artifacts.configured() {
		return nil
	}
	artBucket, err := c.Artifacts.Backend.bucket()
	if err != nil {
		return fmt.Errorf("artifacts.backend: %w", err)
	}
	if c.Backend.Type == "r2" || c.Backend.Type == "s3" {
		stateBucket, err := c.Backend.bucket()
		if err != nil {
			return fmt.Errorf("backend: %w", err)
		}
		if artBucket == stateBucket {
			return fmt.Errorf("artifacts.backend bucket %q must differ from the state backend bucket", artBucket)
		}
	}
	return nil
}

func loadStackConfig(path string) (stackConfig, error) {
	var cfg stackConfig
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("stack config not found: %s", path)
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
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
