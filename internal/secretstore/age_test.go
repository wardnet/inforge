package secretstore_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/secretstore"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	identity, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	plaintext := []byte("postgres://user:pass@host/db?sslmode=require")

	ct, err := secretstore.Encrypt(plaintext, recipient)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ct, "-----BEGIN AGE ENCRYPTED FILE-----"),
		"ciphertext must be ASCII-armored so the committed store diffs as text")

	got, err := secretstore.Decrypt(ct, identity)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

// TestEncryptTrimsRecipientWhitespace proves Encrypt tolerates the trailing
// newline a recipient read from a file or flag value typically carries.
func TestEncryptTrimsRecipientWhitespace(t *testing.T) {
	identity, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)

	ct, err := secretstore.Encrypt([]byte("v"), recipient+"\n")
	require.NoError(t, err)
	got, err := secretstore.Decrypt(ct, identity+"\n")
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), got)
}

func TestDecryptWrongIdentityFails(t *testing.T) {
	_, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	otherIdentity, _, err := secretstore.GenerateIdentity()
	require.NoError(t, err)

	ct, err := secretstore.Encrypt([]byte("secret"), recipient)
	require.NoError(t, err)

	_, err = secretstore.Decrypt(ct, otherIdentity)
	require.Error(t, err)
}

func TestDecryptCorruptArmorFails(t *testing.T) {
	identity, _, err := secretstore.GenerateIdentity()
	require.NoError(t, err)

	_, err = secretstore.Decrypt("not an armored age file", identity)
	require.Error(t, err)
}

func TestEncryptBadRecipientFails(t *testing.T) {
	_, err := secretstore.Encrypt([]byte("v"), "age1notavalidrecipient")
	require.Error(t, err)
}

func TestParseRecipient(t *testing.T) {
	_, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)

	assert.NoError(t, secretstore.ParseRecipient(recipient))
	assert.NoError(t, secretstore.ParseRecipient(recipient+"\n"))
	assert.Error(t, secretstore.ParseRecipient("garbage"))
	// An identity is not a recipient: the two key halves must never be
	// interchangeable (committing the private key would be fatal).
	identity, _, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	assert.Error(t, secretstore.ParseRecipient(identity))
}

func TestIdentityFromEnv(t *testing.T) {
	identity, _, err := secretstore.GenerateIdentity()
	require.NoError(t, err)

	t.Setenv(secretstore.IdentityEnvVar, identity)
	got, err := secretstore.IdentityFromEnv()
	require.NoError(t, err)
	assert.Equal(t, identity, got)

	t.Setenv(secretstore.IdentityEnvVar, "")
	_, err = secretstore.IdentityFromEnv()
	require.ErrorContains(t, err, secretstore.IdentityEnvVar)
}
