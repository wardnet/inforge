package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/hostsecrets"
	"golang.org/x/crypto/ssh"
)

// TestDecryptSecretsBlobRoundTrip encrypts a hostsecrets.Blob to a host SSH key
// the way inforge will, then decrypts it through DecryptSecretsBlob — proving
// the host-key age path the agent relies on (ADR-0035).
func TestDecryptSecretsBlobRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	// Write the private key as an OpenSSH PEM file, like /etc/ssh/ssh_host_ed25519_key.
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ssh_host_ed25519_key")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0o600))

	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	authorizedKey := string(ssh.MarshalAuthorizedKey(sshPub))

	blob := hostsecrets.Blob{Env: map[string]string{"DATABASE_URL": "postgres://x"}}
	ct, err := hostsecrets.EncryptBlob(blob, authorizedKey)
	require.NoError(t, err)

	secretsPath := filepath.Join(dir, "secrets.age")
	require.NoError(t, os.WriteFile(secretsPath, ct, 0o600))

	got, err := DecryptSecretsBlob(secretsPath, keyPath)
	require.NoError(t, err)
	assert.Equal(t, blob, got)
}

func TestDecryptSecretsBlobWrongKeyFails(t *testing.T) {
	dir := t.TempDir()

	// Encrypt to one key...
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	authorizedKey := string(ssh.MarshalAuthorizedKey(sshPub))
	ct, err := hostsecrets.EncryptBlob(hostsecrets.Blob{Env: map[string]string{"K": "v"}}, authorizedKey)
	require.NoError(t, err)
	secretsPath := filepath.Join(dir, "secrets.age")
	require.NoError(t, os.WriteFile(secretsPath, ct, 0o600))

	// ...but decrypt with a different host key.
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pemBlock, err := ssh.MarshalPrivateKey(otherPriv, "")
	require.NoError(t, err)
	keyPath := filepath.Join(dir, "ssh_host_ed25519_key")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0o600))

	_, err = DecryptSecretsBlob(secretsPath, keyPath)
	require.Error(t, err)
}
