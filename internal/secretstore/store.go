package secretstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
// per-container maps under an explicit `containers:` key keeps the recipient
// header unambiguous regardless of container names.
type Store struct {
	// Recipient is the committed X25519 public key (age1…) every value in this
	// store is encrypted to. The matching private identity is the deploy-side
	// INFORGE_SECRETS_KEY.
	Recipient string `yaml:"recipient"`
	// Containers maps container -> KEY -> armored age ciphertext. Per-value
	// ciphertext (not a whole-file blob) keeps diffs reviewable and lets one key
	// rotate without touching its neighbours.
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
	b, err := os.ReadFile(path)
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
	if err := os.WriteFile(path, append([]byte(header), b...), 0o644); err != nil {
		return fmt.Errorf("write secret store: %w", err)
	}
	return nil
}

// Get returns the ciphertext for (container, key), reporting presence.
func (s *Store) Get(container, key string) (string, bool) {
	v, ok := s.Containers[container][key]
	return v, ok
}

// Set stores ciphertext under (container, key), allocating nested maps as
// needed.
func (s *Store) Set(container, key, ciphertext string) {
	if s.Containers == nil {
		s.Containers = map[string]map[string]string{}
	}
	if s.Containers[container] == nil {
		s.Containers[container] = map[string]string{}
	}
	s.Containers[container][key] = ciphertext
}

// Delete removes (container, key), pruning an emptied container map, and
// reports whether the entry existed.
func (s *Store) Delete(container, key string) bool {
	m, ok := s.Containers[container]
	if !ok {
		return false
	}
	if _, ok := m[key]; !ok {
		return false
	}
	delete(m, key)
	if len(m) == 0 {
		delete(s.Containers, container)
	}
	return true
}

// Keys returns the sorted secret keys stored for a container.
func (s *Store) Keys(container string) []string {
	m := s.Containers[container]
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
