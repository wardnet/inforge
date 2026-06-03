// Package manifest assembles a service's manifest from a base plus contributor
// fields, and decides whether the host requires bootstrapping. The trigger is
// the data, not a flag: if any contributed value is marked secret (via Secret),
// those fields are emitted SOPS/age-encrypted and BootstrapNeeded is set (see
// docs/adr/0005).
package manifest

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/wardnet/inforge/internal/bootstrap"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// SecretValue wraps a manifest value to mark it sensitive. Its presence in an
// assembled manifest is what makes a VM require bootstrapping.
type SecretValue struct {
	Value any
}

// Secret marks a manifest value as sensitive.
func Secret(v any) SecretValue {
	return SecretValue{Value: v}
}

// Base is the bootstrap-free core of every manifest.
type Base struct {
	Version   int    `yaml:"version"`
	Region    string `yaml:"region"`
	Namespace string `yaml:"namespace"`
}

// Result is the outcome of assembling a manifest.
type Result struct {
	// Manifest is the rendered manifest YAML. When BootstrapNeeded is true it is
	// a SOPS/age-encrypted document with only the secret fields encrypted.
	Manifest string
	// BootstrapNeeded is true iff the manifest contained at least one secret.
	BootstrapNeeded bool
}

// Generate merges the base and contributions into one manifest. Any secret
// values are unwrapped to plaintext, then — if present — the whole document is
// SOPS/age-encrypted to recipient with only the secret fields encrypted, and
// BootstrapNeeded is set. With no secrets the manifest is emitted as plain YAML.
func Generate(base Base, contributions []types.ManifestContribution, recipient string) (Result, error) {
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

	secretKeys := map[string]bool{}
	unwrapSecrets(merged, secretKeys)

	plain, err := yaml.Marshal(merged)
	if err != nil {
		return Result{}, fmt.Errorf("marshal manifest: %w", err)
	}

	if len(secretKeys) == 0 {
		return Result{Manifest: string(plain), BootstrapNeeded: false}, nil
	}
	// Empty recipient signals a probe call (caller just wants to know whether
	// bootstrapping is needed, without paying for a Mint()). Return early so
	// the caller can decide whether to mint a real key and call again.
	if recipient == "" {
		return Result{BootstrapNeeded: true}, nil
	}

	enc, err := bootstrap.EncryptYAML(plain, recipient, encryptedRegex(secretKeys))
	if err != nil {
		return Result{}, fmt.Errorf("encrypt secret fields: %w", err)
	}
	return Result{Manifest: string(enc), BootstrapNeeded: true}, nil
}

// unwrapSecrets walks a manifest map, recording the key names of secret values
// and replacing each SecretValue with its underlying plaintext (so SOPS sees a
// real value to encrypt). Secrets are matched as map values; nested maps and
// slices are walked.
func unwrapSecrets(m map[string]any, secretKeys map[string]bool) {
	for k, v := range m {
		switch t := v.(type) {
		case SecretValue:
			secretKeys[k] = true
			m[k] = t.Value
		case map[string]any:
			unwrapSecrets(t, secretKeys)
		case []any:
			unwrapSecretsSlice(t, secretKeys)
		}
	}
}

func unwrapSecretsSlice(s []any, secretKeys map[string]bool) {
	for _, e := range s {
		switch t := e.(type) {
		case map[string]any:
			unwrapSecrets(t, secretKeys)
		case []any:
			unwrapSecretsSlice(t, secretKeys)
		}
	}
}

// encryptedRegex builds a SOPS EncryptedRegex anchored to the exact secret key
// names, so only those fields are encrypted.
func encryptedRegex(secretKeys map[string]bool) string {
	keys := make([]string, 0, len(secretKeys))
	for k := range secretKeys {
		keys = append(keys, regexp.QuoteMeta(k))
	}
	sort.Strings(keys)
	return "^(" + strings.Join(keys, "|") + ")$"
}
