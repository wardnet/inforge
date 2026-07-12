package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/types"
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

// twoTierFixture inits a store with explicit recipients (so the test holds both
// the offline root identity and the CI identity) and adds a two-tier PKI.
func twoTierFixture(t *testing.T) (dir, rootIdentity, ciIdentity string) {
	t.Helper()
	dir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o755))
	rootIdentity, rootRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	ciIdentity, ciRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runPkiInit(dir, "prd", ciRecipient, rootRecipient))
	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-mesh", pki.TopologyTwoTier, ""))
	return dir, rootIdentity, ciIdentity
}

func TestRunPkiIntermediateMintsCIHeldChain(t *testing.T) {
	dir, rootIdentity, ciIdentity := twoTierFixture(t)
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)

	require.NoError(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "global"))

	store, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	p, ok := store.Get("wardnet-mesh")
	require.True(t, ok)
	mat, ok := p.Intermediates["global"]
	require.True(t, ok, "intermediate must be recorded under its scope")

	// The intermediate is a path-len-0 CA that chains to the committed root.
	interCert, err := pki.ParseCertificate(mat.Cert)
	require.NoError(t, err)
	assert.True(t, interCert.IsCA)
	assert.True(t, interCert.MaxPathLenZero)
	rootCert, err := pki.ParseCertificate(p.Root.Cert)
	require.NoError(t, err)
	require.NoError(t, interCert.CheckSignatureFrom(rootCert))

	// Custody: the intermediate KEY is CI-held — decrypts with the CI identity...
	plain, err := secretstore.Decrypt(mat.Key, ciIdentity)
	require.NoError(t, err)
	_, err = pki.ParsePrivateKey(string(plain))
	require.NoError(t, err)
	// ...and NOT with the offline root identity (only the root key is cold).
	_, err = secretstore.Decrypt(mat.Key, rootIdentity)
	require.Error(t, err)
}

func TestRunPkiIntermediateRequiresRootKeyEnv(t *testing.T) {
	dir, _, _ := twoTierFixture(t)
	t.Setenv(pki.RootIdentityEnvVar, "")
	err := runPkiIntermediate(dir, "prd", "wardnet-mesh", "global")
	require.ErrorContains(t, err, pki.RootIdentityEnvVar)
}

func TestRunPkiIntermediateWrongRootKey(t *testing.T) {
	dir, _, _ := twoTierFixture(t)
	wrongIdentity, _, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	t.Setenv(pki.RootIdentityEnvVar, wrongIdentity)
	err = runPkiIntermediate(dir, "prd", "wardnet-mesh", "global")
	require.ErrorContains(t, err, "decrypt root key")
}

func TestRunPkiIntermediateRejectsRootOnly(t *testing.T) {
	dir, rootIdentity, _ := twoTierFixture(t)
	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-daemon", pki.TopologyRootOnly, ""))
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)
	err := runPkiIntermediate(dir, "prd", "wardnet-daemon", "global")
	require.ErrorContains(t, err, "only two-tier")
}

func TestRunPkiIntermediateValidation(t *testing.T) {
	dir, rootIdentity, _ := twoTierFixture(t)
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)

	require.ErrorContains(t, runPkiIntermediate(dir, "prd", "nope", "global"), "not found")
	require.ErrorContains(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "  "), "scope is required")

	require.NoError(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "global"))
	require.ErrorContains(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "global"), "already has an intermediate")
}

func TestRunPkiIntermediateBeforeInit(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prd"), 0o755))
	t.Setenv(pki.RootIdentityEnvVar, "irrelevant")
	err := runPkiIntermediate(dir, "prd", "wardnet-mesh", "global")
	require.ErrorContains(t, err, "inforge pki init")
}

func TestRunPkiLsShowsIntermediate(t *testing.T) {
	dir, rootIdentity, _ := twoTierFixture(t)
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)
	require.NoError(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "global"))

	out, err := captureStdout(t, func() error { return runPkiLs(dir, "prd") })
	require.NoError(t, err)
	assert.Contains(t, out, "intermediates: global")
}

// TestRenewMeshCertsAsIgnoresUnrelatedVariables is the regression guard for the
// production failure that motivated the lazy variable resolver: `inforge releases
// deploy tunneller` (and `inforge pki renew`) died with
//
//	error: mint mesh leaf for tunneller: variables.yaml: missing required env var: SSH_AUTHORIZED_KEYS
//
// Minting a leaf reads base_domain and the region slugs — never the ssh block, never
// a provider credential. The old eager loader expanded the whole document on load, so
// an unrelated unset variable failed a code path that does not read it. Only tunneller
// broke because mtls_files: is what makes a release mint a leaf at all; ddns and
// tenants never enter this path.
//
// The mint still needs SSH material to PUSH the leaf, so this does not assert overall
// success — it asserts that whatever happens, it is never the unrelated variable that
// stops us.
func TestRenewMeshCertsAsIgnoresUnrelatedVariables(t *testing.T) {
	dir, rootIdentity, ciIdentity := twoTierFixture(t)
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)
	require.NoError(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "global"))
	require.NoError(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "eu-central"))
	t.Setenv(secretstore.IdentityEnvVar, ciIdentity)

	// base_domain is a literal; the ssh block references variables that are NOT set —
	// exactly the shape of a real variables.yaml in CI's release job.
	t.Setenv("SSH_AUTHORIZED_KEYS", "")
	t.Setenv("DEPLOY_PUBLIC_KEY", "")
	const varsDoc = `base_domain: wardnet.network
ssh:
  authorizedKeys: ${SSH_AUTHORIZED_KEYS}
  deployPublicKey: ${DEPLOY_PUBLIC_KEY}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prd", "variables.yaml"), []byte(varsDoc), 0o644))

	// regions.yaml's provider credentials are unset too. The mint builds no provider
	// and calls no cloud API, so it must not require them either — the same bug on the
	// credentials side, which broke `inforge pki renew` for anyone without a Hetzner
	// token in the environment.
	t.Setenv("HCLOUD_TOKEN", "")
	const regionsDoc = `regions:
  eu-central:
    slug: euc
    providers:
      hetzner:
        apiToken: ${HCLOUD_TOKEN}
global:
  placementRegion: eu-central
  providers:
    hetzner:
      apiToken: ${HCLOUD_TOKEN}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prd", "regions.yaml"), []byte(regionsDoc), 0o644))

	// An mtls_files service is the only kind that mints its own leaf on release.
	global := types.Resources{
		Compute: []types.ComputeSpec{{Name: "edge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "tunneller", Pki: "wardnet-mesh", MtlsFiles: true, Host: "edge"},
		},
	}

	_, err := renewMeshCertsAs(context.Background(), dir, "prd", "prd", global, types.Resources{}, "tunneller", "", io.Discard)

	// The leaf IS minted — we get all the way to pushing it, and only fail there
	// because no host answers SSH in a unit test. Reaching the push proves the mint
	// completed: it resolved base_domain into the host FQDN below, with both the ssh
	// block and the provider credentials unresolvable. That is the fix.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "push leaf.age", "the mint completed and only the SSH push failed")
	assert.Contains(t, err.Error(), "edge.vm.prd.wardnet.network", "base_domain resolved into the host FQDN")
	assert.NotContains(t, err.Error(), "SSH_AUTHORIZED_KEYS", "the mint must not require an ssh variable it never reads")
	assert.NotContains(t, err.Error(), "DEPLOY_PUBLIC_KEY")
	assert.NotContains(t, err.Error(), "HCLOUD_TOKEN", "the mint builds no provider and must not require a credential")
}
