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
		Name:        container + "-svc",
		Container:   container,
		User:        container,
		Environment: map[string]string{},
	}
	for _, k := range keys {
		svc.Environment[k] = "vault:" + k
	}
	return types.Resources{Service: []types.ServiceSpec{svc}}
}

func TestDecryptEncryptedSecretsNoneDeclared(t *testing.T) {
	// A service with only literal secrets (no vault: prefix) must not require
	// a store or key.
	res := types.Resources{Service: []types.ServiceSpec{{
		Name: "ghost-svc", Container: "ghost", User: "ghost",
		Environment: map[string]string{"K": "v"},
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

// reservedFixture writes a store holding one reserved (namespace,key) plaintext.
func reservedFixture(t *testing.T, env, ns, key, plaintext string) (dir, identity string) {
	t.Helper()
	identity, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	dir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, env), 0o755))
	ct, err := secretstore.Encrypt([]byte(plaintext), recipient)
	require.NoError(t, err)
	store := &secretstore.Store{Recipient: recipient}
	store.SetReserved(ns, key, ct)
	require.NoError(t, store.Save(secretstore.Path(dir, env)))
	return dir, identity
}

// TestDecryptReservedSecret: the reserved read is fully decoupled from the
// service `vault:` path (bug #2) — it surfaces the credential with NO service
// referencing it — and degrades to "" (a caller-decided misconfiguration) when
// the store or the entry is absent.
func TestDecryptReservedSecret(t *testing.T) {
	dir, identity := reservedFixture(t, "prd", "observability", "otlp_auth", "id:token")
	t.Setenv(secretstore.IdentityEnvVar, identity)

	// Round-trip with no services declaring vault: at all.
	got, err := decryptReservedSecret(dir, "prd", "observability", "otlp_auth", false)
	require.NoError(t, err)
	assert.Equal(t, "id:token", got)

	// A missing store is not an error here — the caller turns "" into its own
	// "endpoint set but no credential" message.
	got, err = decryptReservedSecret(t.TempDir(), "prd", "observability", "otlp_auth", false)
	require.NoError(t, err)
	assert.Empty(t, got)

	// A present store missing the entry likewise returns "".
	got, err = decryptReservedSecret(dir, "prd", "observability", "MISSING", false)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDecryptReservedSecretKeyless(t *testing.T) {
	dir, _ := reservedFixture(t, "prd", "observability", "otlp_auth", "id:token")
	t.Setenv(secretstore.IdentityEnvVar, "")

	// Keyless preview -> placeholder, no error.
	got, err := decryptReservedSecret(dir, "prd", "observability", "otlp_auth", true)
	require.NoError(t, err)
	assert.Equal(t, encryptedPlaceholder, got)

	// Keyless up -> error (the credential must be real to provision).
	_, err = decryptReservedSecret(dir, "prd", "observability", "otlp_auth", false)
	require.ErrorContains(t, err, secretstore.IdentityEnvVar)
}
