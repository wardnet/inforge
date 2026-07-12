// Package deployment loads the service-repo deployment manifests that tell
// inforge which services to release and where their artifacts live per
// environment. The manifests live in a deployments/ directory alongside the
// service code; inforge never writes to them.
package deployment

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/wardnet/inforge/internal/yamldoc"
)

// Config is the top-level deployments/inforge.yaml. It declares the default
// platform repo and the list of services this repo ships.
type Config struct {
	// Platform is the GitHub repo (owner/repo) that runs inforge for the
	// infrastructure this repo's services are deployed onto.
	// Example: "wardnet/infra"
	Platform string `yaml:"platform"`

	// Services lists the service names enabled in this repo. Each name must
	// have a corresponding <name>.yaml in the same directory.
	Services []string `yaml:"services"`
}

// ServiceDescriptor is a per-service deployments/<name>.yaml. It declares
// per-environment artifact configuration and an optional platform override.
type ServiceDescriptor struct {
	// Platform overrides the top-level Config.Platform for this service only.
	Platform string `yaml:"platform,omitempty"`

	// Environments maps environment names (e.g. "qa", "prd") to the
	// deployment config for that environment.
	Environments map[string]EnvConfig `yaml:"environments"`
}

// EnvConfig holds environment-specific deployment settings for a service.
type EnvConfig struct {
	// ArtifactPath is the local directory whose contents are delivered to the
	// host. Defaults to "dist" when empty.
	ArtifactPath string `yaml:"artifact_path"`

	// HealthCheck is an optional HTTP path checked after the unit restarts.
	HealthCheck string `yaml:"health_check,omitempty"`
}

// LoadConfig reads deployments/inforge.yaml from dir.
func LoadConfig(dir string) (Config, error) {
	var cfg Config
	path := filepath.Join(dir, "inforge.yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- dir is the operator-supplied --deploy-dir flag
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("deployment config not found: %s — run from the repo root or pass --deploy-dir", path)
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
	if cfg.Platform == "" {
		return cfg, fmt.Errorf("%s: platform is required", path)
	}
	if len(cfg.Services) == 0 {
		return cfg, fmt.Errorf("%s: services list is empty", path)
	}
	return cfg, nil
}

// LoadServiceDescriptor reads deployments/<name>.yaml from dir.
func LoadServiceDescriptor(dir, name string) (ServiceDescriptor, error) {
	var desc ServiceDescriptor
	path := filepath.Join(dir, name+".yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- dir/name derive from operator-supplied --deploy-dir/--service flags
	if err != nil {
		if os.IsNotExist(err) {
			return desc, fmt.Errorf("service descriptor not found: %s", path)
		}
		return desc, err
	}
	doc, err := yamldoc.Parse(path, data)
	if err != nil {
		return desc, err
	}
	if err := doc.Decode(&desc); err != nil {
		return desc, err
	}
	if len(desc.Environments) == 0 {
		return desc, fmt.Errorf("%s: environments map is empty", path)
	}
	return desc, nil
}

// Resolve returns the platform and EnvConfig for a given service and
// environment, applying the service-level platform override when present.
func Resolve(cfg Config, desc ServiceDescriptor, service, env string) (platform string, envCfg EnvConfig, err error) {
	platform = cfg.Platform
	if desc.Platform != "" {
		platform = desc.Platform
	}

	envCfg, ok := desc.Environments[env]
	if !ok {
		return "", EnvConfig{}, fmt.Errorf("service %q has no config for environment %q (available: %v)",
			service, env, envKeys(desc.Environments))
	}
	if envCfg.ArtifactPath == "" {
		envCfg.ArtifactPath = "dist"
	}
	return platform, envCfg, nil
}

func envKeys(m map[string]EnvConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
