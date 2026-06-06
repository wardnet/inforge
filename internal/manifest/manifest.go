// Package manifest assembles a compute instance's manifest from a base plus any
// contributor fields and renders it as plain YAML. Secrets are NOT baked into
// the manifest: they are fetched at runtime by inforge-bootstrap (see
// internal/bootstrapper) using a per-service machine identity, so the manifest
// carries only non-secret configuration. The former SOPS/age value-baking and
// first-boot key-broker bootstrap (ADR 0005/0006) have been retired.
package manifest

import (
	"fmt"

	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// Base is the core of every manifest.
type Base struct {
	Version   int    `yaml:"version"`
	Region    string `yaml:"region"`
	Namespace string `yaml:"namespace"`
}

// Generate merges the base and any contributions into a single plain-YAML
// manifest. There is no longer any secret handling: every value is emitted as
// legible YAML.
func Generate(base Base, contributions []types.ManifestContribution) (string, error) {
	merged := map[string]any{
		"version":   base.Version,
		"region":    base.Region,
		"namespace": base.Namespace,
	}
	for _, c := range contributions {
		for k, v := range c {
			merged[k] = v
		}
	}

	out, err := yaml.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	return string(out), nil
}
