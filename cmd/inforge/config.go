package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

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

// r2ToS3URL translates an r2://<bucket> URL to the Pulumi s3:// URL format
// for Cloudflare R2's S3-compatible API.
func r2ToS3URL(raw string) (string, error) {
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
	return cfg, nil
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
