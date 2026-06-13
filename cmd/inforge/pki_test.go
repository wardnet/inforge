package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/secretstore"
)

// pkiFixture builds a resources dir whose env already has a secret store, so
// the PKI store's CI recipient can be reused from it. It returns the dir and
// the secret store's recipient.
func pkiFixture(t *testing.T) (dir, ciRecipient string) {
	t.Helper()
	dir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o755))
	_, ciRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, (&secretstore.Store{Recipient: ciRecipient}).Save(secretstore.Path(dir, "prd")))
	return dir, ciRecipient
}

// captureStdout swaps os.Stdout for the duration of fn and returns what it
// wrote, so the ls/identity output can be asserted.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	runErr := fn()
	require.NoError(t, w.Close())
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func TestRunPkiInitReusesSecretRecipient(t *testing.T) {
	dir, ciRecipient := pkiFixture(t)

	require.NoError(t, runPkiInit(dir, "prd", "", ""))

	store, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	assert.Equal(t, ciRecipient, store.Recipient, "CI recipient must be reused from the secret store")
	assert.NotEmpty(t, store.RootRecipient)
	assert.NotEqual(t, store.Recipient, store.RootRecipient, "the offline root recipient is distinct from the CI recipient")

	// init refuses to clobber an existing store.
	err = runPkiInit(dir, "prd", "", "")
	require.ErrorContains(t, err, "already exists")
}

func TestRunPkiInitNoRecipientSource(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o755))
	err := runPkiInit(dir, "prd", "", "")
	require.ErrorContains(t, err, "inforge secret init")
}

func TestRunPkiInitMissingEnvDir(t *testing.T) {
	err := runPkiInit(t.TempDir(), "prd", "age1ci", "age1root")
	require.ErrorContains(t, err, "does not exist")
}

func TestRunPkiAddTwoTierRootIsCold(t *testing.T) {
	dir, _ := pkiFixture(t)
	rootIdentity, rootRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	ciIdentity, ciRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)

	// Init with both recipients explicit so the test holds both identities.
	require.NoError(t, runPkiInit(dir, "prd", ciRecipient, rootRecipient))
	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-mesh", pki.TopologyTwoTier, ""))

	store, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	p, ok := store.Get("wardnet-mesh")
	require.True(t, ok)
	assert.Equal(t, pki.TopologyTwoTier, p.Topology)

	// The cert is committed in the clear and is a valid CA.
	cert, err := pki.ParseCertificate(p.Root.Cert)
	require.NoError(t, err)
	assert.True(t, cert.IsCA)

	// The cold root key decrypts with the OFFLINE root identity...
	plaintext, err := secretstore.Decrypt(p.Root.Key, rootIdentity)
	require.NoError(t, err)
	_, err = pki.ParsePrivateKey(string(plaintext))
	require.NoError(t, err)

	// ...and is NOT decryptable by the CI identity — CI cannot sign with a cold root.
	_, err = secretstore.Decrypt(p.Root.Key, ciIdentity)
	require.Error(t, err)
}

func TestRunPkiAddRootOnlyKeyIsCIHeld(t *testing.T) {
	dir, _ := pkiFixture(t)
	rootIdentity, rootRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	ciIdentity, ciRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)

	require.NoError(t, runPkiInit(dir, "prd", ciRecipient, rootRecipient))
	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-daemon", pki.TopologyRootOnly, ""))

	store, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	p, ok := store.Get("wardnet-daemon")
	require.True(t, ok)
	assert.Equal(t, "global", p.Scope, "root-only defaults to the global scope")

	// The root-only key is delivered to an online issuer: CI holds it.
	_, err = secretstore.Decrypt(p.Root.Key, ciIdentity)
	require.NoError(t, err)
	// The offline root recipient is NOT used for root-only keys.
	_, err = secretstore.Decrypt(p.Root.Key, rootIdentity)
	require.Error(t, err)
}

func TestRunPkiAddValidation(t *testing.T) {
	dir, _ := pkiFixture(t)

	// add before init.
	require.ErrorContains(t, runPkiAdd(dir, "prd", "x", pki.TopologyTwoTier, ""), "inforge pki init")

	require.NoError(t, runPkiInit(dir, "prd", "", ""))

	require.ErrorContains(t, runPkiAdd(dir, "prd", "x", "", ""), "--topology is required")
	require.ErrorContains(t, runPkiAdd(dir, "prd", "x", "bogus", ""), "unknown topology")
	require.ErrorContains(t, runPkiAdd(dir, "prd", "x", pki.TopologyTwoTier, "global"), "--scope is not valid")

	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-mesh", pki.TopologyTwoTier, ""))
	require.ErrorContains(t, runPkiAdd(dir, "prd", "wardnet-mesh", pki.TopologyTwoTier, ""), "already exists")
}

func TestRunPkiLs(t *testing.T) {
	dir, _ := pkiFixture(t)

	// Listing an uninitialized env errors.
	_, err := captureStdout(t, func() error { return runPkiLs(dir, "prd-empty") })
	require.ErrorContains(t, err, "not found")

	require.NoError(t, runPkiInit(dir, "prd", "", ""))
	out, err := captureStdout(t, func() error { return runPkiLs(dir, "prd") })
	require.NoError(t, err)
	assert.Contains(t, out, "no PKIs")

	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-mesh", pki.TopologyTwoTier, ""))
	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-daemon", pki.TopologyRootOnly, ""))

	out, err = captureStdout(t, func() error { return runPkiLs(dir, "prd") })
	require.NoError(t, err)
	assert.Contains(t, out, "wardnet-mesh")
	assert.Contains(t, out, "two-tier")
	assert.Contains(t, out, "intermediates: (none)")
	assert.Contains(t, out, "wardnet-daemon")
	assert.Contains(t, out, "root-only")
	assert.Contains(t, out, "scope global")
}
