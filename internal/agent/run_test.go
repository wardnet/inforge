package agent

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/hostsecrets"
)

// A service's files: map may span BOTH on-host blobs: an mtls_files: service that
// also holds a pki/* grant draws its leaf/bundle from the renew-owned leaf.age and
// its granted PEMs from the deploy-owned secrets.age. Any path that projects the
// descriptor's whole files: set must therefore resolve it against the MERGED blob.
//
// runProjectLeaf (the `inforge pki renew` push path) decrypted leaf.age ALONE and
// then projected every files: entry, so the moment tunneller gained a pki grant the
// renew push died with `mesh material "pki/daemon-jwt/cert.pem" ... not found or
// empty in the provider` — taking the whole mesh baseline, and hence the deploy,
// down with it. These tests pin the two halves of that contract.
func TestProjectFilesResolvesDescriptorSpanningBothBlobs(t *testing.T) {
	// tunneller's real shape: mtls material from leaf.age, granted PEMs from secrets.age.
	descFiles := map[string]string{
		"MTLS_LEAF_CERT_PATH":           "mtls/leaf.crt",
		"MTLS_LEAF_KEY_PATH":            "mtls/leaf.key",
		"TUNNELLER_JWT_VERIFY_KEY_PATH": "pki/daemon-jwt/cert.pem",
	}
	secretsAge := hostsecrets.Blob{Files: map[string]string{"pki/daemon-jwt/cert.pem": "CERT-PEM"}}
	leafAge := hostsecrets.Blob{Files: map[string]string{
		"mtls/leaf.crt": "LEAF-CERT", "mtls/leaf.key": "LEAF-KEY",
	}}

	// leaf.age ALONE cannot satisfy the set — this is the production failure.
	_, _, err := projectFiles(descFiles, leafAge.Files, t.TempDir(), os.Getuid(), os.Getgid())
	require.Error(t, err, "projecting the full files: set against leaf.age alone must fail")
	assert.Contains(t, err.Error(), "pki/daemon-jwt/cert.pem")

	// Merged, every key resolves and the whole set lands.
	var merged hostsecrets.Blob
	require.NoError(t, mergeSecretsBlob(&merged, secretsAge))
	require.NoError(t, mergeSecretsBlob(&merged, leafAge))

	dir := t.TempDir()
	pathEnv, _, err := projectFiles(descFiles, merged.Files, dir, os.Getuid(), os.Getgid())
	require.NoError(t, err)
	assert.Len(t, pathEnv, 3, "every files: entry yields a <VAR>=<path> pair")
	for _, key := range descFiles {
		assert.FileExists(t, dir+"/"+key)
	}
}

// The merge is what makes the two artifacts safe to combine: secrets.age (deploy)
// and leaf.age (renew) must never claim the same key, so a collision is a producer
// bug and is rejected rather than silently resolved one way.
func TestMergeSecretsBlobRejectsDuplicateKeys(t *testing.T) {
	var merged hostsecrets.Blob
	require.NoError(t, mergeSecretsBlob(&merged, hostsecrets.Blob{
		Env:   map[string]string{"TOKEN": "a"},
		Files: map[string]string{"pki/daemon-jwt/cert.pem": "CERT"},
	}))
	// Disjoint keys merge cleanly.
	require.NoError(t, mergeSecretsBlob(&merged, hostsecrets.Blob{
		Files: map[string]string{"mtls/leaf.crt": "LEAF"},
	}))
	assert.Len(t, merged.Files, 2)

	// A key claimed by both blobs is rejected.
	err := mergeSecretsBlob(&merged, hostsecrets.Blob{
		Files: map[string]string{"pki/daemon-jwt/cert.pem": "OTHER"},
	})
	require.Error(t, err)

	err = mergeSecretsBlob(&merged, hostsecrets.Blob{Env: map[string]string{"TOKEN": "b"}})
	require.Error(t, err)
}
