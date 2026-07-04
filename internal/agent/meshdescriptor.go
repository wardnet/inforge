package agent

import (
	"bytes"
	"fmt"
	"os"

	"github.com/wardnet/inforge/internal/meshpaths"
	"gopkg.in/yaml.v3"
)

// MeshSupportedVersion is the mesh descriptor schema version this agent
// understands. The mesh descriptor is versioned independently of the service
// Descriptor; any field add/remove/rename on MeshDescriptor is a breaking
// change for older agents (strict KnownFields decoding) and must bump this
// (see .agents/rules/bump-descriptor-supported-version-on-schema-change.md).
const MeshSupportedVersion = 1

// MeshDescriptor is the on-host contract for the mesh proxy's cert-material
// pull (ADR-0033): which co-located mesh services' leaves (plus the shared
// trust bundle) `inforge-agent mesh-project` fetches from the provider and
// projects into the mesh proxy's tmpfs RuntimeDir. inforge deploy writes it to
// meshpaths.AgentDir next to the host-key-encrypted provider credential; the
// provider's secret_path is the host's own path in the mesh workspace, and the
// per-host identity behind the credential can read only that path.
type MeshDescriptor struct {
	Version  int      `yaml:"version"`
	Provider Provider `yaml:"provider"`
	// Services are the co-located mesh-member service names, exactly the set the
	// deploy program grouped onto this host (internal/meshplan). Each contributes
	// a <svc>/leaf.crt + <svc>/leaf.key provider key; the host adds bundle.crt.
	Services []string `yaml:"services"`
}

// Files derives the provider-key set the projection fetches and writes, keyed
// and valued by the same relative path (meshpaths.LeafCertKey et al.), so
// projectFiles lands each under RuntimeDir at its meshpaths location.
func (d MeshDescriptor) Files() map[string]string {
	files := map[string]string{meshpaths.BundleKey: meshpaths.BundleKey}
	for _, svc := range d.Services {
		files[meshpaths.LeafCertKey(svc)] = meshpaths.LeafCertKey(svc)
		files[meshpaths.LeafKeyKey(svc)] = meshpaths.LeafKeyKey(svc)
	}
	return files
}

// LoadMeshDescriptor reads and parses the mesh descriptor at path.
func LoadMeshDescriptor(path string) (MeshDescriptor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return MeshDescriptor{}, fmt.Errorf("read mesh descriptor: %w", err)
	}
	return ParseMeshDescriptor(b)
}

// ParseMeshDescriptor parses and validates a mesh descriptor. Unknown fields
// are rejected (an older agent must fail loudly on a newer schema, not
// silently drop fields).
func ParseMeshDescriptor(b []byte) (MeshDescriptor, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)

	var d MeshDescriptor
	if err := dec.Decode(&d); err != nil {
		return MeshDescriptor{}, fmt.Errorf("parse mesh descriptor: %w", err)
	}
	if d.Version != MeshSupportedVersion {
		return MeshDescriptor{}, fmt.Errorf("unsupported mesh descriptor version %d (this agent supports version %d)", d.Version, MeshSupportedVersion)
	}
	// Unlike the service descriptor, the provider is NOT optional: pulling mesh
	// material from it is the descriptor's whole purpose.
	if d.Provider.Kind == "" {
		return MeshDescriptor{}, fmt.Errorf("mesh descriptor: provider.kind is required")
	}
	if len(d.Services) == 0 {
		return MeshDescriptor{}, fmt.Errorf("mesh descriptor: services must be non-empty")
	}
	return d, nil
}
