package agehost_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/wardnet/inforge/internal/agehost"
	"github.com/wardnet/inforge/internal/agent"
	"github.com/wardnet/inforge/internal/hostsecrets"
)

// hostKeyPair returns an SSH host key pair as the two on-disk artifacts inforge
// works with: the public half in authorized_keys form (the contents of a .pub
// file) and the private half as an OpenSSH PEM (the contents of
// /etc/ssh/ssh_host_ed25519_key).
func hostKeyPair(t *testing.T) (pubLine string, privPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	pubLine = string(ssh.MarshalAuthorizedKey(sshPub)) // includes a trailing newline

	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	privPEM = pem.EncodeToMemory(pemBlock)
	return pubLine, privPEM
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	pubLine, privPEM := hostKeyPair(t)
	plaintext := []byte(`{"client_id":"id-123","client_secret":"sec-456"}`)

	ct, err := agehost.Encrypt(plaintext, pubLine)
	require.NoError(t, err)

	got, err := agehost.Decrypt(ct, privPEM)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

// TestEncryptTrimsPublicKeyNewline proves Encrypt tolerates the trailing newline
// that .pub files and `cat`/command Stdout carry — a recipient parse would reject
// it unless trimmed.
func TestEncryptTrimsPublicKeyNewline(t *testing.T) {
	pubLine, privPEM := hostKeyPair(t) // already ends in "\n"
	plaintext := []byte("opaque")

	ct, err := agehost.Encrypt(plaintext, pubLine)
	require.NoError(t, err)
	got, err := agehost.Decrypt(ct, privPEM)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

// TestInteropProgramEncryptAgentDecrypt is the critical program<->agent
// interop test (ADR-0035): the program-side encrypt (hostsecrets.EncryptBlob,
// wrapping agehost.Encrypt, to a host public key) must be readable by the
// agent-side decrypt (agent.DecryptSecretsBlob reading secrets.age with the
// host private key). Both run the real production code paths — no
// re-implementation — so a drift in either half fails here.
func TestInteropProgramEncryptAgentDecrypt(t *testing.T) {
	pubLine, privPEM := hostKeyPair(t)
	blob := hostsecrets.Blob{Env: map[string]string{"client_id": "svc-id", "client_secret": "svc-secret"}}

	// Producer (inforge program): encrypt to the host public key.
	ct, err := hostsecrets.EncryptBlob(blob, pubLine)
	require.NoError(t, err)

	// Lay the bytes out on disk exactly as deploy + the host do.
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.age")
	keyPath := filepath.Join(dir, "ssh_host_ed25519_key")
	require.NoError(t, os.WriteFile(secretsPath, ct, 0o600))
	require.NoError(t, os.WriteFile(keyPath, privPEM, 0o600))

	// Consumer (inforge-agent): decrypt with the host private key.
	got, err := agent.DecryptSecretsBlob(secretsPath, keyPath)
	require.NoError(t, err)
	assert.Equal(t, blob, got)
}

func TestDecryptWrongKeyFails(t *testing.T) {
	pubLine, _ := hostKeyPair(t)
	_, otherPriv := hostKeyPair(t)

	ct, err := agehost.Encrypt([]byte("secret"), pubLine)
	require.NoError(t, err)

	_, err = agehost.Decrypt(ct, otherPriv)
	require.Error(t, err)
}
