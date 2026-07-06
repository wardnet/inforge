// Package hostsecrets is the plaintext-shape and canonical-hash primitive for
// ADR-0035's git-backed, per-host secrets delivery: the resolved value/file map
// that becomes a host's persistent secrets.age, age-encrypted to that host's own
// SSH host key. It wraps internal/agehost (the age-to-SSH-host-key transport)
// rather than reimplementing it, and stays dependency-light and Pulumi-free so
// both program/ and internal/agent can import it without an import cycle.
package hostsecrets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/wardnet/inforge/internal/agehost"
)

// Blob is the plaintext content of a secrets.age: it mirrors the descriptor's
// Env/Files shape (internal/agent.Descriptor.Env/.Files) because a service's
// resolved secrets are env-var-shaped (regular secrets, grant outputs) while
// mesh leaves and mtls material are file-shaped (PEM content keyed by the same
// name the descriptor's Files map names). Both fields are plain
// map[string]string: Env values are secret strings destined for the exec'd
// child's environment; Files values are file contents (e.g. PEM text) destined
// for an on-disk tmpfs write.
type Blob struct {
	Env   map[string]string
	Files map[string]string
}

// canonical is the wire shape Marshal/Unmarshal exchange: a map's key order is
// not part of Go's map type, so canonical re-expresses both maps as
// lexicographically-sorted slices before encoding. This makes the byte-level
// encoding depend only on logical content, never on map iteration order —
// required because Hash's output feeds a Pulumi remote.Command's Triggers
// input, which must be stable given identical content (encoding/json already
// sorts map[string]string keys when marshaling a map directly, but sorting
// explicitly here removes any dependence on that implementation detail).
type canonical struct {
	Env   []kv `json:"env,omitempty"`
	Files []kv `json:"files,omitempty"`
}

type kv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func toCanonical(b Blob) canonical {
	return canonical{Env: sortedKVs(b.Env), Files: sortedKVs(b.Files)}
}

func sortedKVs(m map[string]string) []kv {
	if len(m) == 0 {
		return nil
	}
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Marshal encodes b into the canonical bytes that get age-encrypted into
// secrets.age. Identical logical content always produces identical bytes,
// regardless of map insertion order.
func (b Blob) Marshal() ([]byte, error) {
	data, err := json.Marshal(toCanonical(b))
	if err != nil {
		return nil, fmt.Errorf("marshal secrets blob: %w", err)
	}
	return data, nil
}

// Unmarshal is the inverse of Marshal.
func Unmarshal(data []byte) (Blob, error) {
	var c canonical
	if err := json.Unmarshal(data, &c); err != nil {
		return Blob{}, fmt.Errorf("unmarshal secrets blob: %w", err)
	}
	return Blob{Env: kvsToMap(c.Env), Files: kvsToMap(c.Files)}, nil
}

func kvsToMap(kvs []kv) map[string]string {
	if len(kvs) == 0 {
		return nil
	}
	m := make(map[string]string, len(kvs))
	for _, e := range kvs {
		m[e.Key] = e.Value
	}
	return m
}

// Hash returns the hex-encoded SHA-256 of b's canonical plaintext bytes. Per
// ADR-0035 this is what becomes the Pulumi remote.Command's Triggers input in
// place of the (non-deterministic, freshly-keyed-per-encryption) age
// ciphertext: it must depend only on b's logical content, never on timestamps
// or the ephemeral encryption key introduced later at EncryptBlob.
func (b Blob) Hash() (string, error) {
	data, err := b.Marshal()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// EncryptBlob canonically marshals b and age-encrypts it to sshPublicKey (an
// authorized_keys-form host public key), producing the bytes written to a
// host's secrets.age.
func EncryptBlob(b Blob, sshPublicKey string) ([]byte, error) {
	data, err := b.Marshal()
	if err != nil {
		return nil, err
	}
	ct, err := agehost.Encrypt(data, sshPublicKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt secrets blob: %w", err)
	}
	return ct, nil
}

// DecryptBlob age-decrypts ciphertext with hostKeyPEM (an OpenSSH-PEM host
// private key) and unmarshals the result back into a Blob — the inverse of
// EncryptBlob, run on-host at boot.
func DecryptBlob(ciphertext, hostKeyPEM []byte) (Blob, error) {
	data, err := agehost.Decrypt(ciphertext, hostKeyPEM)
	if err != nil {
		return Blob{}, fmt.Errorf("decrypt secrets blob: %w", err)
	}
	return Unmarshal(data)
}
