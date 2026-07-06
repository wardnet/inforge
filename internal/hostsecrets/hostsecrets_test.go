package hostsecrets_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/wardnet/inforge/internal/hostsecrets"
)

// hostKeyPair mirrors internal/agehost's test helper: an SSH host key pair as
// the two on-disk artifacts inforge works with, the public half in
// authorized_keys form and the private half as an OpenSSH PEM.
func hostKeyPair(t *testing.T) (pubLine string, privPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	pubLine = string(ssh.MarshalAuthorizedKey(sshPub))

	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	privPEM = pem.EncodeToMemory(pemBlock)
	return pubLine, privPEM
}

func TestHashStableAcrossMapInsertionOrder(t *testing.T) {
	env1 := map[string]string{"A": "1", "B": "2", "C": "3"}
	env2 := map[string]string{"C": "3", "A": "1", "B": "2"}

	h1, err := hostsecrets.Blob{Env: env1}.Hash()
	require.NoError(t, err)
	h2, err := hostsecrets.Blob{Env: env2}.Hash()
	require.NoError(t, err)
	assert.Equal(t, h1, h2)
}

func TestHashStableAcrossRepeatedCalls(t *testing.T) {
	b := hostsecrets.Blob{
		Env:   map[string]string{"DB_URL": "postgres://x", "API_KEY": "abc"},
		Files: map[string]string{"leaf.pem": "-----BEGIN CERTIFICATE-----"},
	}

	h1, err := b.Hash()
	require.NoError(t, err)
	h2, err := b.Hash()
	require.NoError(t, err)
	assert.Equal(t, h1, h2)
}

func TestHashSensitiveToContentChange(t *testing.T) {
	base := hostsecrets.Blob{Env: map[string]string{"A": "1"}}
	changedValue := hostsecrets.Blob{Env: map[string]string{"A": "2"}}
	changedKey := hostsecrets.Blob{Env: map[string]string{"B": "1"}}
	extraKey := hostsecrets.Blob{Env: map[string]string{"A": "1", "B": "1"}}
	withFile := hostsecrets.Blob{Env: map[string]string{"A": "1"}, Files: map[string]string{"f": "1"}}

	hBase, err := base.Hash()
	require.NoError(t, err)

	for name, other := range map[string]hostsecrets.Blob{
		"changed value": changedValue,
		"changed key":   changedKey,
		"extra key":     extraKey,
		"with file":     withFile,
	} {
		t.Run(name, func(t *testing.T) {
			h, err := other.Hash()
			require.NoError(t, err)
			assert.NotEqual(t, hBase, h)
		})
	}
}

func TestHashEmptyAndNilBlobEquivalent(t *testing.T) {
	nilBlob := hostsecrets.Blob{}
	emptyBlob := hostsecrets.Blob{Env: map[string]string{}, Files: map[string]string{}}

	hNil, err := nilBlob.Hash()
	require.NoError(t, err)
	hEmpty, err := emptyBlob.Hash()
	require.NoError(t, err)
	assert.Equal(t, hNil, hEmpty)
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	cases := map[string]hostsecrets.Blob{
		"empty": {},
		"env only": {
			Env: map[string]string{"FOO": "bar", "BAZ": "qux"},
		},
		"files only": {
			Files: map[string]string{"trust_bundle.pem": "cert-a\ncert-b"},
		},
		"both": {
			Env:   map[string]string{"PASSWORD": "s3cr3t"},
			Files: map[string]string{"leaf.pem": "leaf", "leaf.key": "key"},
		},
	}

	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := b.Marshal()
			require.NoError(t, err)

			got, err := hostsecrets.Unmarshal(data)
			require.NoError(t, err)

			wantEnv := b.Env
			if len(wantEnv) == 0 {
				wantEnv = nil
			}
			wantFiles := b.Files
			if len(wantFiles) == 0 {
				wantFiles = nil
			}
			assert.Equal(t, wantEnv, got.Env)
			assert.Equal(t, wantFiles, got.Files)
		})
	}
}

func TestUnmarshalRejectsGarbage(t *testing.T) {
	_, err := hostsecrets.Unmarshal([]byte("not json"))
	assert.Error(t, err)
}

func TestEncryptDecryptBlobRoundTrip(t *testing.T) {
	pubLine, privPEM := hostKeyPair(t)
	want := hostsecrets.Blob{
		Env:   map[string]string{"DB_PASSWORD": "hunter2", "API_TOKEN": "tok"},
		Files: map[string]string{"leaf.pem": "cert-bytes", "leaf.key": "key-bytes"},
	}

	ct, err := hostsecrets.EncryptBlob(want, pubLine)
	require.NoError(t, err)

	got, err := hostsecrets.DecryptBlob(ct, privPEM)
	require.NoError(t, err)
	assert.Equal(t, want.Env, got.Env)
	assert.Equal(t, want.Files, got.Files)
}

func TestEncryptDecryptEmptyBlobRoundTrip(t *testing.T) {
	pubLine, privPEM := hostKeyPair(t)

	ct, err := hostsecrets.EncryptBlob(hostsecrets.Blob{}, pubLine)
	require.NoError(t, err)

	got, err := hostsecrets.DecryptBlob(ct, privPEM)
	require.NoError(t, err)
	assert.Nil(t, got.Env)
	assert.Nil(t, got.Files)
}

func TestDecryptBlobWrongKeyFails(t *testing.T) {
	pubLine, _ := hostKeyPair(t)
	_, otherPriv := hostKeyPair(t)

	ct, err := hostsecrets.EncryptBlob(hostsecrets.Blob{Env: map[string]string{"A": "1"}}, pubLine)
	require.NoError(t, err)

	_, err = hostsecrets.DecryptBlob(ct, otherPriv)
	assert.Error(t, err)
}

func TestEncryptBlobDeterministicPlaintextDifferentCiphertext(t *testing.T) {
	pubLine, privPEM := hostKeyPair(t)
	b := hostsecrets.Blob{Env: map[string]string{"A": "1"}}

	ct1, err := hostsecrets.EncryptBlob(b, pubLine)
	require.NoError(t, err)
	ct2, err := hostsecrets.EncryptBlob(b, pubLine)
	require.NoError(t, err)

	assert.NotEqual(t, ct1, ct2, "age encryption uses a fresh ephemeral key per call")

	got1, err := hostsecrets.DecryptBlob(ct1, privPEM)
	require.NoError(t, err)
	got2, err := hostsecrets.DecryptBlob(ct2, privPEM)
	require.NoError(t, err)
	assert.Equal(t, got1, got2)
}
