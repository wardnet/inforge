package agent

import (
	"fmt"
	"os"

	"github.com/wardnet/inforge/internal/hostsecrets"
)

// defaultHostKeyPath is the host's SSH private key, used as the age identity that
// decrypts a host's secrets.age/leaf.age blobs. inforge encrypts each blob to
// the matching host public key, so only this host can read it and no extra key
// material is provisioned.
const defaultHostKeyPath = "/etc/ssh/ssh_host_ed25519_key"

// DecryptSecretsBlob reads the age-encrypted hostsecrets.Blob at path and
// decrypts it in memory using the host SSH key at hostKeyPath. The actual
// host-key age primitive lives in internal/hostsecrets (wrapping
// internal/agehost), shared with the inforge program that produces the blob —
// so producer and consumer round-trip the same implementation. The plaintext
// is returned to the caller and never written to disk.
func DecryptSecretsBlob(path, hostKeyPath string) (hostsecrets.Blob, error) {
	keyPEM, err := os.ReadFile(hostKeyPath) // #nosec G304 -- hostKeyPath is the fixed defaultHostKeyPath constant
	if err != nil {
		return hostsecrets.Blob{}, fmt.Errorf("read host key: %w", err)
	}
	ciphertext, err := os.ReadFile(path) // #nosec G304 -- path is built from the deploy-tool-controlled on-host service dir and a fixed filename
	if err != nil {
		return hostsecrets.Blob{}, fmt.Errorf("read secrets blob: %w", err)
	}
	return hostsecrets.DecryptBlob(ciphertext, keyPEM)
}
