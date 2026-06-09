package main

import "testing"

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

func TestValidateArtifacts(t *testing.T) {
	t.Run("unconfigured is fine", func(t *testing.T) {
		c := projectConfig{Backend: backendConfig{Type: "r2", URL: "r2://state"}}
		if err := c.validateArtifacts(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("distinct bucket is fine", func(t *testing.T) {
		c := projectConfig{
			Backend:   backendConfig{Type: "r2", URL: "r2://wardnet-state"},
			Artifacts: artifactsConfig{Backend: backendConfig{Type: "r2", URL: "r2://wardnet-artifacts"}, Keep: 10},
		}
		if err := c.validateArtifacts(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("same bucket as state is rejected", func(t *testing.T) {
		c := projectConfig{
			Backend:   backendConfig{Type: "r2", URL: "r2://wardnet-state"},
			Artifacts: artifactsConfig{Backend: backendConfig{Type: "r2", URL: "r2://wardnet-state"}},
		}
		if err := c.validateArtifacts(); err == nil {
			t.Fatal("expected error for artifacts bucket == state bucket")
		}
	})

	t.Run("non-bucket state backend skips the comparison", func(t *testing.T) {
		// A file-backed state has no bucket; artifacts in r2 must still validate.
		c := projectConfig{
			Backend:   backendConfig{Type: "file", URL: "file://.pulumi"},
			Artifacts: artifactsConfig{Backend: backendConfig{Type: "r2", URL: "r2://wardnet-artifacts"}},
		}
		if err := c.validateArtifacts(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
