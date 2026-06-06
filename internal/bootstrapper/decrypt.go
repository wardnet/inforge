package bootstrapper

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
	"filippo.io/age/agessh"
)

// defaultHostKeyPath is the host's SSH private key, used as the age identity that
// decrypts the per-service credential blob. inforge encrypts the credential to
// the matching host public key, so only this host can read it and no extra key
// material is provisioned.
const defaultHostKeyPath = "/etc/ssh/ssh_host_ed25519_key"

// DecryptCredential reads the age-encrypted credential at path and decrypts it in
// memory using the host SSH key at hostKeyPath (plain age, not SOPS — the blob is
// opaque). The plaintext is returned to the caller and never written to disk.
func DecryptCredential(path, hostKeyPath string) ([]byte, error) {
	keyPEM, err := os.ReadFile(hostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read host key: %w", err)
	}
	identity, err := agessh.ParseIdentity(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse host key as age identity: %w", err)
	}

	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credential: %w", err)
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	plaintext, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read decrypted credential: %w", err)
	}
	return plaintext, nil
}
