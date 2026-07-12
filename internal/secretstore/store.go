package secretstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the store file's name inside an environment's resources
// directory (resources/<env>/secrets.enc.yaml).
const FileName = "secrets.enc.yaml"

// header is prepended to every saved store file. The store is machine-managed:
// hand-edits cannot produce valid ciphertext and would only corrupt entries.
const header = "# managed by `inforge secret` — do not edit by hand\n"

// ErrNotFound reports that the store file does not exist for the environment.
var ErrNotFound = errors.New("secret store not found")

// Store is the decoded form of an environment's committed secret store. Every
// value is an ASCII-armored age ciphertext encrypted to Recipient; nesting the
// per-service maps under an explicit `services:` key keeps the recipient header
// unambiguous regardless of service names.
type Store struct {
	// Recipient is the committed X25519 public key (age1…) every value in this
	// store is encrypted to. The matching private identity is the deploy-side
	// INFORGE_SECRETS_KEY.
	Recipient string `yaml:"recipient"`
	// Services maps service -> KEY -> armored age ciphertext (ADR-0040). The key
	// is the SERVICE, not its container: the blast radius of a secret is the unit
	// that rotates it, and two services sharing a container each hold their own
	// entry. Per-value ciphertext (not a whole-file blob) keeps diffs reviewable
	// and lets one key rotate without touching its neighbours.
	Services map[string]map[string]string `yaml:"services,omitempty"`
	// Reserved holds inforge-internal, env-level secrets that are NOT service
	// secrets — they are referenced by no service and consumed directly by the
	// deploy (e.g. the observability OTLP credential, ADR-0031). Keeping them in
	// their own namespace (keyed by reserved-namespace -> KEY) means a user
	// service may freely use ANY name — a service called "observability" never
	// collides with the OTLP credential — and the deploy read no longer has to be
	// routed through some service's `vault:` reference.
	Reserved map[string]map[string]string `yaml:"reserved,omitempty"`
	// Containers is the pre-ADR-0040 container-keyed map, retained ONLY so Load
	// can detect and reject a store that has not been migrated. It has no
	// accessors and is never written back. Without the field, yaml.v3's
	// non-strict decode would silently DROP a `containers:` block: the store
	// would parse clean, and every service would deploy with no secrets at all.
	Containers map[string]map[string]string `yaml:"containers,omitempty"`
}

// Path returns the store file path for an environment under the resources dir.
func Path(dir, env string) string {
	return filepath.Join(dir, env, FileName)
}

// Load reads and decodes a store file. A missing file is reported as
// ErrNotFound (callers distinguish "not initialized" from a broken store); a
// present store without a recipient is corrupt, since nothing could have
// encrypted its values.
func Load(path string) (*Store, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- path derives from the operator's --dir flag and env argument
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("read secret store: %w", err)
	}
	var s Store
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse secret store %s: %w", path, err)
	}
	if s.Recipient == "" {
		return nil, fmt.Errorf("secret store %s has no recipient — the file is corrupt or was not created by `inforge secret init`", path)
	}
	// A store still carrying the pre-ADR-0040 `containers:` block has not been
	// migrated. Fail here rather than in one caller, so validate, the CLI and the
	// deploy all inherit the same guard — a store that parsed clean while silently
	// serving no secrets would take every service down at once.
	if len(s.Containers) > 0 {
		names := make([]string, 0, len(s.Containers))
		for c := range s.Containers {
			names = append(names, c)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("secret store %s still has a `containers:` block (%s) — secrets are keyed by SERVICE now, not by container (ADR-0040). Move each entry under `services:` keyed by the service that declares the `vault:` reference, duplicating a value shared by two services; the ciphertext is unchanged, so this is a plain YAML edit", path, strings.Join(names, ", "))
	}
	return &s, nil
}

// Save writes the store back to path (0644 — ciphertext only, safe to commit).
// Output is deterministic: yaml.v3 marshals map keys sorted, and the
// round-trip test locks that in so rotations diff as single-entry changes.
func (s *Store) Save(path string) error {
	b, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode secret store: %w", err)
	}
	if err := os.WriteFile(path, append([]byte(header), b...), 0o600); err != nil {
		return fmt.Errorf("write secret store: %w", err)
	}
	return nil
}

// Get returns the ciphertext for a service's (service, key).
func (s *Store) Get(service, key string) (string, bool) { return nsGet(s.Services, service, key) }

// GetReserved returns the ciphertext for a reserved (namespace, key).
func (s *Store) GetReserved(namespace, key string) (string, bool) {
	return nsGet(s.Reserved, namespace, key)
}

// Set stores ciphertext under a service's (service, key).
func (s *Store) Set(service, key, ciphertext string) {
	s.Services = nsSet(s.Services, service, key, ciphertext)
}

// SetReserved stores ciphertext under a reserved (namespace, key).
func (s *Store) SetReserved(namespace, key, ciphertext string) {
	s.Reserved = nsSet(s.Reserved, namespace, key, ciphertext)
}

// Delete removes a service's (service, key), pruning an emptied map, and
// reports whether the entry existed.
func (s *Store) Delete(service, key string) bool { return nsDelete(s.Services, service, key) }

// DeleteReserved removes a reserved (namespace, key), pruning an emptied map.
func (s *Store) DeleteReserved(namespace, key string) bool {
	return nsDelete(s.Reserved, namespace, key)
}

// Keys returns the sorted secret keys stored for a service.
func (s *Store) Keys(service string) []string { return nsKeys(s.Services, service) }

// ReservedKeys returns the sorted secret keys stored for a reserved namespace.
func (s *Store) ReservedKeys(namespace string) []string { return nsKeys(s.Reserved, namespace) }

// nsGet/nsSet/nsDelete/nsKeys are the shared two-level-map operations behind the
// service and reserved accessors, so the two namespaces cannot drift on the
// nil-allocation, empty-map pruning, or key-sorting behaviour.
func nsGet(m map[string]map[string]string, ns, key string) (string, bool) {
	v, ok := m[ns][key]
	return v, ok
}

func nsSet(m map[string]map[string]string, ns, key, ciphertext string) map[string]map[string]string {
	if m == nil {
		m = map[string]map[string]string{}
	}
	if m[ns] == nil {
		m[ns] = map[string]string{}
	}
	m[ns][key] = ciphertext
	return m
}

func nsDelete(m map[string]map[string]string, ns, key string) bool {
	sub, ok := m[ns]
	if !ok {
		return false
	}
	if _, ok := sub[key]; !ok {
		return false
	}
	delete(sub, key)
	if len(sub) == 0 {
		delete(m, ns)
	}
	return true
}

func nsKeys(m map[string]map[string]string, ns string) []string {
	sub := m[ns]
	keys := make([]string, 0, len(sub))
	for k := range sub {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
