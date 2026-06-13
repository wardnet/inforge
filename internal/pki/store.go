package pki

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// FileName is the store file's name inside an environment's resources
// directory (resources/<env>/pki.enc.yaml).
const FileName = "pki.enc.yaml"

// header is prepended to every saved store file. The store is machine-managed:
// hand-edits cannot produce valid ciphertext keys and would only corrupt it.
const header = "# managed by `inforge pki` — do not edit by hand\n"

// Topology values: the two trust-tree shapes of ADR-0024.
const (
	// TopologyTwoTier is a cold (offline) root plus one intermediate per active
	// scope; inforge mints leaves from the scope's intermediate at deploy. The
	// root key is encrypted to the offline rootRecipient — CI never holds it.
	// This is the service mesh.
	TopologyTwoTier = "two-tier"
	// TopologyRootOnly is a root with no intermediate and no inforge leaf
	// minting; the root key is delivered to a designated online issuer. The root
	// key is encrypted to the CI recipient. This is the daemon PKI.
	TopologyRootOnly = "root-only"
)

// ErrNotFound reports that the store file does not exist for the environment.
var ErrNotFound = errors.New("pki store not found")

// ValidTopology reports whether t is a recognized trust-tree shape. It is the
// single point of truth for the valid set, shared by the CLI (which adds
// empty-vs-unknown messaging) and Load (which rejects malformed stores up
// front); later slices that branch on topology validate through it too.
func ValidTopology(t string) bool {
	switch t {
	case TopologyTwoTier, TopologyRootOnly:
		return true
	default:
		return false
	}
}

// Material is one certificate and its private key. Cert is plaintext PEM (it is
// public, committed verbatim so the whole hierarchy is auditable in git); Key
// is an ASCII-armored age ciphertext, encrypted to whichever recipient owns the
// tier (rootRecipient for cold roots, recipient for CI-held keys).
type Material struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// PKI is one named certificate hierarchy in the store.
type PKI struct {
	// Topology is TopologyTwoTier or TopologyRootOnly.
	Topology string `yaml:"topology"`
	// Scope is the single scope a root-only PKI serves (e.g. "global"); it is
	// empty for two-tier, whose scopes are carried by Intermediates.
	Scope string `yaml:"scope,omitempty"`
	// Root is the trust anchor: cert committed in the clear, key encrypted to
	// rootRecipient (two-tier) or recipient (root-only).
	Root Material `yaml:"root"`
	// Intermediates maps scope ("global" or an abstract region) -> its
	// intermediate CA. Empty until slice #106 mints them; only two-tier uses it.
	Intermediates map[string]Material `yaml:"intermediates,omitempty"`
}

// Store is the decoded form of an environment's committed PKI store. Its
// structure — names, topologies, scopes, and every certificate — is readable
// with no key at all, so `inforge validate` can check service references
// against it exactly as it checks `vault:` against the secret store.
type Store struct {
	// RootRecipient is the committed X25519 public key (age1…) two-tier cold
	// root keys are encrypted to. Its matching private identity is held offline
	// by an operator (RootIdentityEnvVar) and is never present in CI, so CI
	// cannot sign with a cold root.
	RootRecipient string `yaml:"rootRecipient"`
	// Recipient is the CI master recipient (the same age1… recorded in the
	// secret store), whose identity is INFORGE_SECRETS_KEY. Root-only issuer
	// keys are encrypted to it for deploy-time delivery.
	Recipient string `yaml:"recipient"`
	// PKIs maps PKI name -> hierarchy.
	PKIs map[string]PKI `yaml:"pkis,omitempty"`
}

// RootKeyRecipient returns the age recipient a PKI's root key is encrypted to:
// the offline rootRecipient for a cold two-tier root (CI never holds the
// matching identity), the CI recipient for a root-only root (delivered to an
// online issuer at deploy). This is the single encrypt-side custody rule; the
// matching decrypt identity is selected per call site (offline operator for
// intermediate minting, deploy for leaf minting in #108).
func (s *Store) RootKeyRecipient(topology string) string {
	if topology == TopologyRootOnly {
		return s.Recipient
	}
	return s.RootRecipient
}

// IntermediateKeyRecipient returns the recipient intermediate keys are
// encrypted to — always the CI recipient. Intermediates are minted offline by
// the cold root but used by deploy to sign leaves, so CI must be able to
// decrypt them (ADR-0024).
func (s *Store) IntermediateKeyRecipient() string {
	return s.Recipient
}

// Path returns the store file path for an environment under the resources dir.
func Path(dir, env string) string {
	return filepath.Join(dir, env, FileName)
}

// Load reads and decodes a store file. A missing file is reported as
// ErrNotFound (callers distinguish "not initialized" from a broken store); a
// present store missing either recipient is corrupt, since nothing could have
// encrypted its keys.
func Load(path string) (*Store, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("read pki store: %w", err)
	}
	var s Store
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse pki store %s: %w", path, err)
	}
	if s.RootRecipient == "" || s.Recipient == "" {
		return nil, fmt.Errorf("pki store %s is missing a recipient — the file is corrupt or was not created by `inforge pki init`", path)
	}
	for name, p := range s.PKIs {
		if !ValidTopology(p.Topology) {
			return nil, fmt.Errorf("pki store %s: PKI %q has unknown topology %q — the file is corrupt or was written by a newer inforge", path, name, p.Topology)
		}
	}
	return &s, nil
}

// Save writes the store back to path (0644 — public certs and ciphertext keys
// only, safe to commit). Output is deterministic: yaml.v3 marshals map keys
// sorted, so adding one PKI diffs as a single-entry change.
func (s *Store) Save(path string) error {
	b, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode pki store: %w", err)
	}
	if err := os.WriteFile(path, append([]byte(header), b...), 0o644); err != nil {
		return fmt.Errorf("write pki store: %w", err)
	}
	return nil
}

// Get returns the PKI of the given name, reporting presence.
func (s *Store) Get(name string) (PKI, bool) {
	p, ok := s.PKIs[name]
	return p, ok
}

// Set stores a PKI under name, allocating the map as needed.
func (s *Store) Set(name string, p PKI) {
	if s.PKIs == nil {
		s.PKIs = map[string]PKI{}
	}
	s.PKIs[name] = p
}

// Names returns the sorted PKI names in the store.
func (s *Store) Names() []string {
	names := make([]string, 0, len(s.PKIs))
	for n := range s.PKIs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
