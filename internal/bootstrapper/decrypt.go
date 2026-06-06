package bootstrapper

import (
	"fmt"
	"os"

	"github.com/wardnet/inforge/internal/agehost"
)

// defaultHostKeyPath is the host's SSH private key, used as the age identity that
// decrypts the per-service credential blob. inforge encrypts the credential to
// the matching host public key, so only this host can read it and no extra key
// material is provisioned.
const defaultHostKeyPath = "/etc/ssh/ssh_host_ed25519_key"

// DecryptCredential reads the age-encrypted credential at path and decrypts it in
// memory using the host SSH key at hostKeyPath. The actual host-key age primitive
// lives in internal/agehost, shared with the inforge program that produces the
// blob — so producer and consumer round-trip the same implementation. The
// plaintext is returned to the caller and never written to disk.
func DecryptCredential(path, hostKeyPath string) ([]byte, error) {
	keyPEM, err := os.ReadFile(hostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read host key: %w", err)
	}
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credential: %w", err)
	}
	return agehost.Decrypt(ciphertext, keyPEM)
}
