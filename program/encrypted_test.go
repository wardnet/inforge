package program

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/types"
)

// encryptedFixture writes a resources/<env>/secrets.enc.yaml holding the given
// plaintexts encrypted to a fresh recipient, and returns the resources dir and
// the matching master identity.
func encryptedFixture(t *testing.T, env string, values map[string]map[string]string) (dir, identity string) {
	t.Helper()
	identity, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)

	dir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, env), 0o755))
	store := &secretstore.Store{Recipient: recipient}
	for container, kv := range values {
		for key, plaintext := range kv {
			ct, err := secretstore.Encrypt([]byte(plaintext), recipient)
			require.NoError(t, err)
			store.Set(container, key, ct)
		}
	}
	require.NoError(t, store.Save(secretstore.Path(dir, env)))
	return dir, identity
}

func encryptedSpec(container string, keys ...string) types.Resources {
	svc := types.ServiceSpec{
		Name:      container + "-svc",
		Container: container,
		User:      container,
		Secrets:   map[string]string{},
	}
	for _, k := range keys {
		svc.Secrets[k] = "vault:" + k
	}
	return types.Resources{Service: []types.ServiceSpec{svc}}
}

func TestDecryptEncryptedSecretsNoneDeclared(t *testing.T) {
	// A service with only literal secrets (no vault: prefix) must not require
	// a store or key.
	res := types.Resources{Service: []types.ServiceSpec{{
		Name: "ghost-svc", Container: "ghost", User: "ghost",
		Secrets: map[string]string{"K": "v"},
	}}}
	got, err := decryptEncryptedSecrets(res, types.Resources{}, t.TempDir(), "prd", false)
	require.NoError(t, err)
	assert.Nil(t, got, "an env without encrypted sources must not require a store or key")
}

func TestDecryptEncryptedSecretsRoundTrip(t *testing.T) {
	dir, identity := encryptedFixture(t, "prd", map[string]map[string]string{
		"ghost":  {"API_KEY": "plain-api-key"},
		"bridge": {"TOKEN": "plain-token"},
	})
	t.Setenv(secretstore.IdentityEnvVar, identity)

	// bridge's spec arrives via the global slice to prove both slices contribute.
	got, err := decryptEncryptedSecrets(encryptedSpec("ghost", "API_KEY"), encryptedSpec("bridge", "TOKEN"), dir, "prd", false)
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]string{
		"ghost":  {"API_KEY": "plain-api-key"},
		"bridge": {"TOKEN": "plain-token"},
	}, got)
}

func TestDecryptEncryptedSecretsKeylessPreview(t *testing.T) {
	dir, _ := encryptedFixture(t, "prd", map[string]map[string]string{"ghost": {"API_KEY": "v"}})
	t.Setenv(secretstore.IdentityEnvVar, "")

	got, err := decryptEncryptedSecrets(encryptedSpec("ghost", "API_KEY"), types.Resources{}, dir, "prd", true)
	require.NoError(t, err, "a keyless preview must not fail")
	assert.Equal(t, encryptedPlaceholder, got["ghost"]["API_KEY"])
}

func TestDecryptEncryptedSecretsKeylessUpFails(t *testing.T) {
	dir, _ := encryptedFixture(t, "prd", map[string]map[string]string{"ghost": {"API_KEY": "v"}})
	t.Setenv(secretstore.IdentityEnvVar, "")

	_, err := decryptEncryptedSecrets(encryptedSpec("ghost", "API_KEY"), types.Resources{}, dir, "prd", false)
	require.ErrorContains(t, err, secretstore.IdentityEnvVar)
}

func TestDecryptEncryptedSecretsMissingStore(t *testing.T) {
	_, err := decryptEncryptedSecrets(encryptedSpec("ghost", "API_KEY"), types.Resources{}, t.TempDir(), "prd", false)
	require.ErrorContains(t, err, "does not exist")
}

func TestDecryptEncryptedSecretsMissingCiphertext(t *testing.T) {
	dir, identity := encryptedFixture(t, "prd", map[string]map[string]string{"ghost": {"OTHER": "v"}})
	t.Setenv(secretstore.IdentityEnvVar, identity)

	_, err := decryptEncryptedSecrets(encryptedSpec("ghost", "API_KEY"), types.Resources{}, dir, "prd", false)
	require.ErrorContains(t, err, "no ciphertext")
}
