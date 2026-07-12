package agent

import (
	"fmt"
	"os"

	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/yamldoc"
)

// MeshSupportedVersion is the mesh descriptor schema version this agent
// understands. The mesh descriptor is versioned independently of the service
// Descriptor; any field add/remove/rename on MeshDescriptor is a breaking
// change for older agents (strict KnownFields decoding) and must bump this
// (see .agents/rules/bump-descriptor-supported-version-on-schema-change.md).
// v2 removed the Provider block (ADR-0035): the mesh proxy's leaf material is
// delivered directly, as a host-key-encrypted leaf.age, not fetched from a
// runtime secrets provider.
const MeshSupportedVersion = 2

// MeshDescriptor is the on-host contract naming which co-located mesh
// services' leaves (plus the shared trust bundle) the mesh proxy expects in
// its decrypted leaf.age (ADR-0035). inforge deploy writes it, secret-free,
// to meshpaths.AgentDir; `inforge pki renew` separately SSH-pushes the actual
// leaf.age content it describes.
type MeshDescriptor struct {
	Version int `yaml:"version"`
	// Services are the co-located mesh-member service names, exactly the set the
	// deploy program grouped onto this host (internal/meshplan). Each contributes
	// a <svc>/leaf.crt + <svc>/leaf.key key in leaf.age's Files map; the host
	// adds bundle.crt.
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
	b, err := os.ReadFile(path) // #nosec G304 -- path is built from the deploy-tool-controlled on-host directory and a fixed filename
	if err != nil {
		return MeshDescriptor{}, fmt.Errorf("read mesh descriptor: %w", err)
	}
	return ParseMeshDescriptor(b)
}

// ParseMeshDescriptor parses and validates a mesh descriptor. Unknown fields
// are rejected (an older agent must fail loudly on a newer schema, not
// silently drop fields).
func ParseMeshDescriptor(b []byte) (MeshDescriptor, error) {
	doc, err := yamldoc.Parse("mesh descriptor", b)
	if err != nil {
		return MeshDescriptor{}, err
	}
	// STRICT: an older agent handed a newer schema must fail loudly, not drop fields.
	var d MeshDescriptor
	if err := doc.DecodeStrict(&d); err != nil {
		return MeshDescriptor{}, err
	}
	if d.Version != MeshSupportedVersion {
		return MeshDescriptor{}, fmt.Errorf("unsupported mesh descriptor version %d (this agent supports version %d)", d.Version, MeshSupportedVersion)
	}
	if len(d.Services) == 0 {
		return MeshDescriptor{}, fmt.Errorf("mesh descriptor: services must be non-empty")
	}
	return d, nil
}
