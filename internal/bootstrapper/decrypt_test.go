package bootstrapper

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// TestDecryptCredentialRoundTrip encrypts a credential to a host SSH key the way
// inforge will, then decrypts it through DecryptCredential — proving the
// host-key age path the bootstrapper relies on (and, by construction, the format
// PR 2b must produce).
func TestDecryptCredentialRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	// Write the private key as an OpenSSH PEM file, like /etc/ssh/ssh_host_ed25519_key.
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ssh_host_ed25519_key")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0o600))

	// Encrypt a plaintext credential to the matching public key.
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	recipient, err := agessh.NewEd25519Recipient(sshPub)
	require.NoError(t, err)

	plaintext := []byte(`{"client_id":"id-123","client_secret":"sec-456"}`)
	var ct bytes.Buffer
	w, err := age.Encrypt(&ct, recipient)
	require.NoError(t, err)
	_, err = w.Write(plaintext)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	credPath := filepath.Join(dir, "credential.age")
	require.NoError(t, os.WriteFile(credPath, ct.Bytes(), 0o600))

	got, err := DecryptCredential(credPath, keyPath)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

func TestDecryptCredentialWrongKeyFails(t *testing.T) {
	dir := t.TempDir()

	// Encrypt to one key...
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	recipient, err := agessh.NewEd25519Recipient(sshPub)
	require.NoError(t, err)
	var ct bytes.Buffer
	w, err := age.Encrypt(&ct, recipient)
	require.NoError(t, err)
	_, err = w.Write([]byte("secret"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	credPath := filepath.Join(dir, "credential.age")
	require.NoError(t, os.WriteFile(credPath, ct.Bytes(), 0o600))

	// ...but decrypt with a different host key.
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pemBlock, err := ssh.MarshalPrivateKey(otherPriv, "")
	require.NoError(t, err)
	keyPath := filepath.Join(dir, "ssh_host_ed25519_key")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0o600))

	_, err = DecryptCredential(credPath, keyPath)
	require.Error(t, err)
}
