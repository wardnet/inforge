package deployment

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "inforge.yaml", `
platform: wardnet/infra
services:
  - api-server
  - worker
`)
	cfg, err := LoadConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, "wardnet/infra", cfg.Platform)
	assert.Equal(t, []string{"api-server", "worker"}, cfg.Services)
}

func TestLoadConfigMissingPlatform(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "inforge.yaml", "services:\n  - api\n")
	_, err := LoadConfig(dir)
	assert.ErrorContains(t, err, "platform is required")
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := LoadConfig(t.TempDir())
	assert.ErrorContains(t, err, "deployment config not found")
}

func TestLoadServiceDescriptor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "api-server.yaml", `
environments:
  qa:
    artifact_path: dist/debug/
    health_check: /health
  prd:
    artifact_path: dist/release/
`)
	desc, err := LoadServiceDescriptor(dir, "api-server")
	require.NoError(t, err)
	assert.Equal(t, "dist/debug/", desc.Environments["qa"].ArtifactPath)
	assert.Equal(t, "/health", desc.Environments["qa"].HealthCheck)
	assert.Equal(t, "dist/release/", desc.Environments["prd"].ArtifactPath)
}

func TestLoadServiceDescriptorWithPlatformOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "worker.yaml", `
platform: wardnet/infra-workers
environments:
  qa:
    artifact_path: bin/
`)
	desc, err := LoadServiceDescriptor(dir, "worker")
	require.NoError(t, err)
	assert.Equal(t, "wardnet/infra-workers", desc.Platform)
}

func TestResolveAppliesServiceOverride(t *testing.T) {
	cfg := Config{Platform: "wardnet/infra", Services: []string{"worker"}}
	desc := ServiceDescriptor{
		Platform:     "wardnet/infra-workers",
		Environments: map[string]EnvConfig{"qa": {ArtifactPath: "bin/"}},
	}
	platform, envCfg, err := Resolve(cfg, desc, "worker", "qa")
	require.NoError(t, err)
	assert.Equal(t, "wardnet/infra-workers", platform)
	assert.Equal(t, "bin/", envCfg.ArtifactPath)
}

func TestResolveDefaultsArtifactPath(t *testing.T) {
	cfg := Config{Platform: "wardnet/infra", Services: []string{"api"}}
	desc := ServiceDescriptor{
		Environments: map[string]EnvConfig{"qa": {}},
	}
	_, envCfg, err := Resolve(cfg, desc, "api", "qa")
	require.NoError(t, err)
	assert.Equal(t, "dist", envCfg.ArtifactPath)
}

func TestResolveUnknownEnv(t *testing.T) {
	cfg := Config{Platform: "wardnet/infra", Services: []string{"api"}}
	desc := ServiceDescriptor{
		Environments: map[string]EnvConfig{"qa": {ArtifactPath: "dist/"}},
	}
	_, _, err := Resolve(cfg, desc, "api", "staging")
	assert.ErrorContains(t, err, `no config for environment "staging"`)
}
