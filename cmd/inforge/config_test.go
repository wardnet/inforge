package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveStackConfig(t *testing.T) {
	// A defaulted path whose file is absent is NOT an error: the environment comes
	// from the stack name, so the stack config file is optional.
	t.Run("defaulted missing is empty, not an error", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cfg, err := resolveStackConfig("", "prd")
		if err != nil {
			t.Fatalf("want nil error for absent default file, got %v", err)
		}
		if len(cfg.Config) != 0 {
			t.Fatalf("want empty config, got %v", cfg.Config)
		}
	})

	// A defaulted path whose file IS present is loaded.
	t.Run("defaulted present is loaded", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		if err := os.WriteFile(filepath.Join(dir, "inforge.prd.yaml"),
			[]byte("config:\n  foo: bar\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := resolveStackConfig("", "prd")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Config["foo"] != "bar" {
			t.Fatalf("want foo=bar, got %v", cfg.Config)
		}
	})

	// An EXPLICIT --stack-config that is missing is still a hard error: the caller
	// asked for that specific file, so its absence is a real mistake.
	t.Run("explicit missing is an error", func(t *testing.T) {
		_, err := resolveStackConfig(filepath.Join(t.TempDir(), "nope.yaml"), "prd")
		if err == nil {
			t.Fatal("want error for absent explicit file, got nil")
		}
	})
}

func TestBackendBucket(t *testing.T) {
	cases := []struct {
		name    string
		b       backendConfig
		want    string
		wantErr bool
	}{
		{"r2", backendConfig{Type: "r2", URL: "r2://wardnet-artifacts"}, "wardnet-artifacts", false},
		{"s3", backendConfig{Type: "s3", URL: "s3://my-bucket/prefix"}, "my-bucket", false},
		{"file has no bucket", backendConfig{Type: "file", URL: "file://.pulumi"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.b.bucket()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got bucket %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("bucket = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateBuckets(t *testing.T) {
	t.Run("unconfigured is fine", func(t *testing.T) {
		c := projectConfig{Backend: backendConfig{Type: "r2", URL: "r2://state"}}
		if err := c.validateBuckets(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("distinct bucket is fine", func(t *testing.T) {
		c := projectConfig{
			Backend:   backendConfig{Type: "r2", URL: "r2://wardnet-state"},
			Artifacts: artifactsConfig{Backend: backendConfig{Type: "r2", URL: "r2://wardnet-artifacts"}, Keep: 10},
		}
		if err := c.validateBuckets(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("same bucket as state is rejected", func(t *testing.T) {
		c := projectConfig{
			Backend:   backendConfig{Type: "r2", URL: "r2://wardnet-state"},
			Artifacts: artifactsConfig{Backend: backendConfig{Type: "r2", URL: "r2://wardnet-state"}},
		}
		if err := c.validateBuckets(); err == nil {
			t.Fatal("expected error for artifacts bucket == state bucket")
		}
	})

	t.Run("non-bucket state backend skips the comparison", func(t *testing.T) {
		// A file-backed state has no bucket; artifacts in r2 must still validate.
		c := projectConfig{
			Backend:   backendConfig{Type: "file", URL: "file://.pulumi"},
			Artifacts: artifactsConfig{Backend: backendConfig{Type: "r2", URL: "r2://wardnet-artifacts"}},
		}
		if err := c.validateBuckets(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("distinct backups bucket is fine", func(t *testing.T) {
		c := projectConfig{
			Backend:   backendConfig{Type: "r2", URL: "r2://wardnet-state"},
			Artifacts: artifactsConfig{Backend: backendConfig{Type: "r2", URL: "r2://wardnet-artifacts"}},
			Backups:   backupsConfig{Bucket: "wardnet-backups"},
		}
		if err := c.validateBuckets(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("backups bucket equal to state is rejected", func(t *testing.T) {
		c := projectConfig{
			Backend: backendConfig{Type: "r2", URL: "r2://wardnet-state"},
			Backups: backupsConfig{Bucket: "wardnet-state"},
		}
		if err := c.validateBuckets(); err == nil {
			t.Fatal("expected error for backups bucket == state bucket")
		}
	})

	t.Run("backups bucket equal to artifacts is rejected", func(t *testing.T) {
		c := projectConfig{
			Backend:   backendConfig{Type: "r2", URL: "r2://wardnet-state"},
			Artifacts: artifactsConfig{Backend: backendConfig{Type: "r2", URL: "r2://wardnet-artifacts"}},
			Backups:   backupsConfig{Bucket: "wardnet-artifacts"},
		}
		if err := c.validateBuckets(); err == nil {
			t.Fatal("expected error for backups bucket == artifacts bucket")
		}
	})

	t.Run("backups bucket as an r2:// URL is rejected", func(t *testing.T) {
		c := projectConfig{
			Backend: backendConfig{Type: "r2", URL: "r2://wardnet-state"},
			Backups: backupsConfig{Bucket: "r2://wardnet-backups"},
		}
		if err := c.validateBuckets(); err == nil {
			t.Fatal("expected error for backups.bucket that is a URL, not a bare name")
		}
	})

	t.Run("backups bucket with a slash is rejected", func(t *testing.T) {
		c := projectConfig{
			Backend: backendConfig{Type: "r2", URL: "r2://wardnet-state"},
			Backups: backupsConfig{Bucket: "wardnet-backups/prefix"},
		}
		if err := c.validateBuckets(); err == nil {
			t.Fatal("expected error for backups.bucket with a path")
		}
	})

	t.Run("backups bucket with file-backed state is fine", func(t *testing.T) {
		// A file-backed state has no bucket; a backups bucket must still validate.
		c := projectConfig{
			Backend: backendConfig{Type: "file", URL: "file://.pulumi"},
			Backups: backupsConfig{Bucket: "wardnet-backups"},
		}
		if err := c.validateBuckets(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// inforge.yaml is read through the one reader now: a corrupt file must fail with its
// path, and an absent one with the actionable "run from the repo root" message.
func TestLoadProjectConfigMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inforge.yaml")
	if err := os.WriteFile(path, []byte("name: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := mustErr(t, func() error { _, e := loadProjectConfig(path); return e })
	if !strings.Contains(err.Error(), "inforge.yaml") {
		t.Errorf("error must name the file: %v", err)
	}
}

func TestLoadProjectConfigTypeMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inforge.yaml")
	if err := os.WriteFile(path, []byte("name:\n  - not a string\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = mustErr(t, func() error { _, e := loadProjectConfig(path); return e })
}

func TestLoadProjectConfigAbsent(t *testing.T) {
	err := mustErr(t, func() error {
		_, e := loadProjectConfig(filepath.Join(t.TempDir(), "inforge.yaml"))
		return e
	})
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("an absent config must say so: %v", err)
	}
}

func TestLoadStackConfigMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stack.yaml")
	if err := os.WriteFile(path, []byte("config: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := mustErr(t, func() error { _, e := loadStackConfig(path); return e })
	if !strings.Contains(err.Error(), "stack.yaml") {
		t.Errorf("error must name the file: %v", err)
	}
}

func TestLoadStackConfigAbsent(t *testing.T) {
	err := mustErr(t, func() error {
		_, e := loadStackConfig(filepath.Join(t.TempDir(), "stack.yaml"))
		return e
	})
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("an absent stack config must say so: %v", err)
	}
}

// mustErr runs fn, fails the test if it succeeds, and returns the error.
func mustErr(t *testing.T, fn func() error) error {
	t.Helper()
	err := fn()
	if err == nil {
		t.Fatal("expected an error, got none")
	}
	return err
}
