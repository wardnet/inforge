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
	"golang.org/x/crypto/ssh"

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

// hostKey writes an OpenSSH host private key and returns its path plus the
// matching authorized-key line inforge encrypts blobs to.
func hostKey(t *testing.T, dir string) (keyPath, recipient string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	keyPath = filepath.Join(dir, "ssh_host_ed25519_key")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0o600))
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return keyPath, string(ssh.MarshalAuthorizedKey(sshPub))
}

func writeBlob(t *testing.T, path string, blob hostsecrets.Blob, recipient string) {
	t.Helper()
	ct, err := hostsecrets.EncryptBlob(blob, recipient)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, ct, 0o600))
}

// The end-to-end regression: drive the real `inforge-agent project-leaf` path for a
// service shaped like tunneller (mtls_files: + a pki grant), and prove it projects
// the FULL files: set — the granted PEM from secrets.age alongside the leaf material
// from leaf.age.
//
// Before the fix this path decrypted leaf.age alone and died with
// `mesh material "pki/daemon-jwt/cert.pem" ... not found or empty in the provider`,
// which failed the renew push and took the whole mesh baseline (and the deploy) with it.
func TestProjectLeafProjectsFilesSpanningBothBlobs(t *testing.T) {
	dir := t.TempDir()
	runtime := t.TempDir()
	keyPath, recipient := hostKey(t, t.TempDir())

	require.NoError(t, os.WriteFile(filepath.Join(dir, descriptorFile), []byte(`version: 7
service: tunneller
exec: /srv/wardnet/tunneller/run
user: tunneller
env:
    TOKEN: TOKEN
files:
    MTLS_LEAF_CERT_PATH: mtls/leaf.crt
    MTLS_LEAF_KEY_PATH: mtls/leaf.key
    TUNNELLER_JWT_VERIFY_KEY_PATH: pki/daemon-jwt/cert.pem
deployment:
    region: eu-central
    region_slug: euc
    environment: prd
    base_domain: wardnet.network
    fqdn: tunneller.svc.prd.euc.wardnet.network
    host_id: wardnet-prd-euc-vm-edge-01
`), 0o644))

	// Deploy-owned: the granted PEM. Renew-owned: the mtls leaf material.
	writeBlob(t, filepath.Join(dir, secretsFile), hostsecrets.Blob{
		Env:   map[string]string{"TOKEN": "t"},
		Files: map[string]string{"pki/daemon-jwt/cert.pem": "CERT-PEM"},
	}, recipient)
	writeBlob(t, filepath.Join(dir, leafFile), hostsecrets.Blob{
		Files: map[string]string{"mtls/leaf.crt": "LEAF-CERT", "mtls/leaf.key": "LEAF-KEY"},
	}, recipient)

	err := projectLeaf(dir, hostEnv{
		hostKeyPath: keyPath,
		runtimeDir:  func(string) string { return runtime },
		lookupUser:  func(string) (userInfo, error) { return userInfo{uid: os.Getuid(), gid: os.Getgid()}, nil },
	})
	require.NoError(t, err, "the renew push must resolve keys from BOTH blobs")

	// The granted PEM (secrets.age) lands beside the leaf material (leaf.age).
	for path, want := range map[string]string{
		"pki/daemon-jwt/cert.pem": "CERT-PEM",
		"mtls/leaf.crt":           "LEAF-CERT",
		"mtls/leaf.key":           "LEAF-KEY",
	} {
		got, err := os.ReadFile(filepath.Join(runtime, path))
		require.NoError(t, err, "%s must be projected", path)
		assert.Equal(t, want, string(got))
	}
}

// No leaf.age yet (before the first renew push) is a no-op, not an error — driven
// through the real runProjectLeaf entry point, so its wiring to the live host
// dependencies is exercised too.
func TestRunProjectLeafNoLeafIsNoOp(t *testing.T) {
	require.NoError(t, runProjectLeaf(t.TempDir()))
	require.NoError(t, projectLeaf(t.TempDir(), hostEnv{hostKeyPath: "/nonexistent"}))
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
