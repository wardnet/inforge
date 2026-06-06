// Package bootstrapper is the runtime core of the inforge-bootstrap binary: the
// systemd ExecStart for every inforge-managed service. It runs as root, reads a
// per-service on-host descriptor (no secrets), decrypts the service's secrets
// provider credential with the host SSH key, logs in to the provider, fetches
// the service's secrets, injects them as environment variables, drops privilege
// to the service user, and execs the real service binary — so nothing secret is
// ever written to disk, the journal, or argv.
//
// The package is split so the security-sensitive privilege-drop/exec is the only
// platform-gated piece (exec.go is linux-only; exec_other.go stubs it elsewhere)
// and everything else — descriptor parsing, fetch + backoff, decrypt, env
// building, passwd resolution — stays cross-platform and unit-testable without
// root.
package bootstrapper

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// supportedVersion is the descriptor schema major this bootstrapper understands.
// A descriptor declaring any other version fails the start, so a fleet running
// mixed bootstrapper builds never silently misreads a newer descriptor.
const supportedVersion = 1

// Descriptor is the versioned, secret-free on-host contract inforge writes to
// /etc/wardnet/services/<svc>/descriptor.yaml (0644 root). It names the service,
// the binary to exec, the run-as user, the secrets provider coordinates, and the
// env-var -> vault-key mapping (keys are relative to provider secret_path, with
// an infra/ or custom/ prefix encoding origin). It carries no secret values.
type Descriptor struct {
	Version  int               `yaml:"version"`
	Service  string            `yaml:"service"`
	Exec     string            `yaml:"exec"`
	User     string            `yaml:"user"`
	Provider Provider          `yaml:"provider"`
	Env      map[string]string `yaml:"env"`
}

// LoadDescriptor reads and parses the descriptor at path.
func LoadDescriptor(path string) (Descriptor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Descriptor{}, fmt.Errorf("read descriptor: %w", err)
	}
	return ParseDescriptor(b)
}

// ParseDescriptor decodes and validates a descriptor document. Decoding is
// strict (unknown fields are rejected) so an operator typo in a hand-placed
// descriptor fails fast rather than silently dropping a key. An unsupported
// schema version is rejected before any other validation.
func ParseDescriptor(b []byte) (Descriptor, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)

	var d Descriptor
	if err := dec.Decode(&d); err != nil {
		return Descriptor{}, fmt.Errorf("parse descriptor: %w", err)
	}
	if d.Version != supportedVersion {
		return Descriptor{}, fmt.Errorf("unsupported descriptor version %d (this bootstrapper supports version %d)", d.Version, supportedVersion)
	}
	if d.Service == "" {
		return Descriptor{}, fmt.Errorf("descriptor: service is required")
	}
	if d.Exec == "" {
		return Descriptor{}, fmt.Errorf("descriptor: exec is required")
	}
	if d.User == "" {
		return Descriptor{}, fmt.Errorf("descriptor: user is required")
	}
	if d.Provider.Kind == "" {
		return Descriptor{}, fmt.Errorf("descriptor: provider.kind is required")
	}
	return d, nil
}
