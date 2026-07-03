// Package agent is the runtime core of the inforge-agent binary: the
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
package agent

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// SupportedVersion is the descriptor schema major this agent understands.
// A descriptor declaring any other version fails the start, so a fleet running
// mixed agent builds never silently misreads a newer descriptor. inforge
// (the producer) stamps this same constant into every descriptor it writes, so
// producer and consumer can never disagree on the schema version. Because parsing
// is strict (KnownFields), any field addition is a breaking change for an older
// reader, so it bumps this major: v2 added the Deployment block, v3 added the
// Files map, and v4 swapped Deployment.Namespace for Deployment.HostID; v5 added the
// cloud/host resource-identity fields (CloudProvider/CloudRegion/AvailabilityZone/
// MachineType, ADR-0030); v6 added the Mesh block (the east-west service-mesh
// endpoint contract — INFORGE_MESH_URL/PORT/SCOPE, ADR-0032). An older agent
// meeting a newer descriptor fails cleanly on the version rather than on an
// unknown field.
const SupportedVersion = 6

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
	// Files maps an env-var name → a provider secret key (relative to the
	// provider secret_path). For a mesh service, inforge writes the leaf/key/CA
	// bundle to the provider and lists them here; the agent fetches each,
	// writes the PEM to a tmpfs file, and sets the env var to that path (#109).
	// Empty for services with no mesh PKI material.
	Files      map[string]string `yaml:"files,omitempty"`
	Deployment Deployment        `yaml:"deployment"`
	// Mesh is the east-west service-mesh endpoint contract (ADR-0032), present
	// only for a mesh member (a pki: service). The agent injects it as the
	// INFORGE_MESH_* env vars a service reads to make and receive mesh calls; nil
	// for a non-mesh service, which emits none of them.
	Mesh *Mesh `yaml:"mesh,omitempty"`
}

// Mesh is a mesh member's endpoint contract, injected as INFORGE_MESH_* env vars
// (ADR-0032). URL is the loopback base the service dials for ALL outbound mesh
// calls (its own egress endpoint on the local mesh proxy); the target service is
// named per-request in the X-Mesh-Target header, never in the URL. Port is the
// loopback port the service binds to RECEIVE mesh traffic (its mesh.port); it is
// omitted for an egress-only member (a pki: service with no mesh: block, which
// makes outbound calls but exposes nothing inbound). Scope is the member's mesh
// scope — a region name (e.g. "us-east-1") or the literal "global" — used to form
// the caller identity the service demuxes on (INFORGE_MESH_SCOPE).
type Mesh struct {
	URL   string `yaml:"url"`            // INFORGE_MESH_URL — http://127.0.0.1:<egress port>
	Port  int    `yaml:"port,omitempty"` // INFORGE_MESH_PORT — the service's mesh.port (0 => omitted, egress-only)
	Scope string `yaml:"scope"`          // INFORGE_MESH_SCOPE — region name or "global"
}

// Deployment is the secret-free deployment context inforge derives for a service
// and the agent injects as INFORGE_* environment variables (alongside, and
// independent of, the service's secrets). Every value is derived from the
// environment, region, service and host — none is a secret — so it is delivered in
// the plain descriptor and is present for secret-less services too.
type Deployment struct {
	Region      string `yaml:"region"`      // abstract region, e.g. "us-east-1"
	RegionSlug  string `yaml:"region_slug"` // region slug, e.g. "use1"
	Environment string `yaml:"environment"` // environment name, e.g. "prd"
	BaseDomain  string `yaml:"base_domain"` // e.g. "wardnet.network"
	FQDN        string `yaml:"fqdn"`        // service public FQDN, "<service>.svc.<env>.<slug>.<base>"
	// HostID is the full VM resource name of the host this service runs on,
	// "wardnet-<env>-<slug>-vm-<name>-<NN>" (e.g. "wardnet-prd-use1-vm-bridge-01").
	// It is stable per host (does NOT change across restarts), so it is injected as
	// INFORGE_HOST_ID — the OTel host.id resource attribute.
	HostID string `yaml:"host_id"`
	// The four fields below are provider-supplied cloud/host resource identity
	// (ADR-0030), injected as INFORGE_CLOUD_*/INFORGE_HOST_TYPE → the OTel
	// cloud.provider/cloud.region/cloud.availability_zone/host.type attributes.
	// Each is omitempty: a provider that does not supply one writes nothing.
	CloudProvider    string `yaml:"cloud_provider,omitempty"`    // cloud.provider, e.g. "hetzner"
	CloudRegion      string `yaml:"cloud_region,omitempty"`      // cloud.region, e.g. "us-east"
	AvailabilityZone string `yaml:"availability_zone,omitempty"` // cloud.availability_zone, e.g. "ash"
	MachineType      string `yaml:"machine_type,omitempty"`      // host.type, e.g. "cx23"
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
	if d.Version != SupportedVersion {
		return Descriptor{}, fmt.Errorf("unsupported descriptor version %d (this agent supports version %d)", d.Version, SupportedVersion)
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
	// provider is optional: a descriptor with no provider.kind is a secret-less
	// service. It MUST then carry no env mapping — there is nothing to resolve the
	// keys against — so an env without a provider is a producer bug, rejected here.
	if d.Provider.Kind == "" && len(d.Env) > 0 {
		return Descriptor{}, fmt.Errorf("descriptor: env is set but provider.kind is empty (a secret-less service must have no env entries)")
	}
	// files: are provider secret keys too — like env, they are meaningless with no
	// provider to fetch them from, so the same producer-bug guard applies.
	if d.Provider.Kind == "" && len(d.Files) > 0 {
		return Descriptor{}, fmt.Errorf("descriptor: files is set but provider.kind is empty (mesh material requires a provider to fetch it)")
	}
	return d, nil
}
