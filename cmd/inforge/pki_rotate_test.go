package main

import (
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/meshcert"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/secretstore"
)

// mintedIntermediate inits a two-tier PKI with a global intermediate and returns
// the dir plus the offline root and CI identities the test holds.
func mintedIntermediate(t *testing.T) (dir, rootIdentity, ciIdentity string) {
	t.Helper()
	dir, rootIdentity, ciIdentity = twoTierFixture(t)
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)
	require.NoError(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "global"))
	return dir, rootIdentity, ciIdentity
}

// mintLeafFromStore mints a leaf for service in scope from the store's
// intermediate, using the CI identity — the same path deploy/renew take.
func mintLeafFromStore(t *testing.T, store *pki.Store, pkiName, scope, ciIdentity, service string) *x509.Certificate {
	t.Helper()
	leafPEM, _, err := meshcert.MintServiceLeaf(store, pkiName, scope, ciIdentity, "wardnet.network", "prd", service)
	require.NoError(t, err)
	return mustParse(t, leafPEM)
}

func TestRunPkiRotateLeafIsInformational(t *testing.T) {
	out, err := captureStdout(t, func() error { return runPkiRotateLeaf("prd", "wardnet-mesh") })
	require.NoError(t, err)
	assert.Contains(t, out, "inforge pki renew prd")
	assert.Contains(t, out, "expiry IS revocation")
}

func TestRunPkiRotateIntermediateReplacesKey(t *testing.T) {
	dir, _, ciIdentity := mintedIntermediate(t)

	before, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	p, _ := before.Get("wardnet-mesh")
	oldCert := p.Intermediates["global"].Cert

	require.NoError(t, runPkiRotateIntermediate(dir, "prd", "wardnet-mesh", "global"))

	after, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	p, _ = after.Get("wardnet-mesh")
	newMat := p.Intermediates["global"]
	assert.NotEqual(t, oldCert, newMat.Cert, "rotation must replace the intermediate cert")

	// The fresh key is CI-held and the new cert chains to the (unchanged) root.
	newInter := mustParse(t, newMat.Cert)
	rootCert := mustParse(t, p.Root.Cert)
	require.NoError(t, newInter.CheckSignatureFrom(rootCert))
	oldInter := mustParse(t, oldCert)
	assert.NotEqual(t, oldInter.PublicKey, newInter.PublicKey, "rotation rolls the intermediate key")

	// The new key decrypts with the CI identity (deploy mints leaves from it).
	plain, err := secretstore.Decrypt(newMat.Key, ciIdentity)
	require.NoError(t, err)
	_, err = pki.ParsePrivateKey(string(plain))
	require.NoError(t, err)
}

func TestRunPkiRotateIntermediateRequiresExisting(t *testing.T) {
	dir, rootIdentity, _ := twoTierFixture(t)
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)
	// No intermediate minted yet for this scope.
	err := runPkiRotateIntermediate(dir, "prd", "wardnet-mesh", "us-east-1")
	require.ErrorContains(t, err, "no intermediate for scope")
}

func TestRunPkiRecoverIntermediateReplacesAndWarns(t *testing.T) {
	dir, _, _ := mintedIntermediate(t)

	before, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	p, _ := before.Get("wardnet-mesh")
	oldCert := p.Intermediates["global"].Cert

	out, err := captureStdout(t, func() error {
		return runPkiRecoverIntermediate(dir, "prd", "wardnet-mesh", "global")
	})
	require.NoError(t, err)
	assert.Contains(t, out, "BURNED")
	assert.Contains(t, out, "wardnet-<svc>-renew.service")

	after, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	p, _ = after.Get("wardnet-mesh")
	assert.NotEqual(t, oldCert, p.Intermediates["global"].Cert)
}

// TestRunPkiRotateRootDualRootOverlap is the acceptance test: after a root
// rotation, the same live leaf verifies under BOTH the old and the new root.
func TestRunPkiRotateRootDualRootOverlap(t *testing.T) {
	dir, _, ciIdentity := mintedIntermediate(t)
	require.NoError(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "us-east-1"))

	before, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	p, _ := before.Get("wardnet-mesh")
	oldRootCert := mustParse(t, p.Root.Cert)
	oldGlobalInterPEM := p.Intermediates["global"].Cert

	// Mint a leaf from the global intermediate BEFORE rotating (a "live" leaf).
	leaf := mintLeafFromStore(t, before, "wardnet-mesh", "global", ciIdentity, "tenant")

	// Rotate the root (dual-root overlap begins).
	out, err := captureStdout(t, func() error {
		return runPkiRotateRoot(dir, "prd", "wardnet-mesh", false)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "dual-root overlap is now active")
	assert.Contains(t, out, "re-signed 2 intermediate(s)")

	after, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	p, _ = after.Get("wardnet-mesh")

	// The store now holds both roots, and the old global intermediate is retained.
	require.Len(t, p.PreviousRoots, 1)
	assert.Equal(t, oldRootCert.Raw, mustParse(t, p.PreviousRoots[0].Cert).Raw)
	require.Len(t, p.PreviousIntermediates["global"], 1)
	assert.Equal(t, oldGlobalInterPEM, p.PreviousIntermediates["global"][0])
	assert.Len(t, p.RootCerts(), 2, "both roots are anchors during the overlap")

	// The intermediate KEY was preserved (only the cert/issuer changed).
	newGlobalInter := mustParse(t, p.Intermediates["global"].Cert)
	assert.Equal(t, mustParse(t, oldGlobalInterPEM).PublicKey, newGlobalInter.PublicKey)

	newRootCert := mustParse(t, p.Root.Cert)
	oldGlobalInter := mustParse(t, oldGlobalInterPEM)

	// THE OVERLAP PROPERTY: the same leaf verifies under both roots.
	require.NoError(t, verifyChain(leaf, oldGlobalInter, oldRootCert), "old chain still valid")
	require.NoError(t, verifyChain(leaf, newGlobalInter, newRootCert), "new chain valid")

	// A leaf minted AFTER rotation (from the preserved-key intermediate) also
	// verifies under the new root.
	freshLeaf := mintLeafFromStore(t, after, "wardnet-mesh", "global", ciIdentity, "tenant")
	require.NoError(t, verifyChain(freshLeaf, newGlobalInter, newRootCert))
}

func TestRunPkiRotateRootCustodyAndFinalize(t *testing.T) {
	dir, _, _ := mintedIntermediate(t)

	// Wrong offline identity is rejected (only the root custodian may rotate).
	wrong, _, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	t.Setenv(pki.RootIdentityEnvVar, wrong)
	require.ErrorContains(t, runPkiRotateRoot(dir, "prd", "wardnet-mesh", false), "decrypt current root key")

	// A fresh fixture with the real root identity to rotate cleanly.
	dir2, rootIdentity, _ := mintedIntermediate(t)
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)
	require.NoError(t, runPkiRotateRoot(dir2, "prd", "wardnet-mesh", false))

	// A second start before finalizing is refused.
	require.ErrorContains(t, runPkiRotateRoot(dir2, "prd", "wardnet-mesh", false), "already in a root overlap")

	// Finalize drops the retained old root and old intermediate certs.
	out, err := captureStdout(t, func() error { return runPkiRotateRoot(dir2, "prd", "wardnet-mesh", true) })
	require.NoError(t, err)
	assert.Contains(t, out, "finalized root rotation")

	store, err := pki.Load(pki.Path(dir2, "prd"))
	require.NoError(t, err)
	p, _ := store.Get("wardnet-mesh")
	assert.Empty(t, p.PreviousRoots)
	assert.Empty(t, p.PreviousIntermediates)
	assert.Len(t, p.RootCerts(), 1)

	// Finalize with no overlap in progress errors.
	require.ErrorContains(t, runPkiRotateRoot(dir2, "prd", "wardnet-mesh", true), "nothing to finalize")
}

func TestRunPkiRotateIntermediateRejectedDuringRootOverlap(t *testing.T) {
	dir, rootIdentity, _ := mintedIntermediate(t)
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)
	require.NoError(t, runPkiRotateRoot(dir, "prd", "wardnet-mesh", false))

	// Rotating or recovering an intermediate mid-overlap would orphan the
	// old-key leaves the overlap protects — both are refused until --finalize.
	require.ErrorContains(t, runPkiRotateIntermediate(dir, "prd", "wardnet-mesh", "global"), "root overlap")
	require.ErrorContains(t, runPkiRecoverIntermediate(dir, "prd", "wardnet-mesh", "global"), "root overlap")
	// Minting a brand-new scope mid-overlap is refused too — it would sign under
	// the new root only, invisible to consumers still on the old root.
	require.ErrorContains(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "us-east-1"), "root overlap")

	// After finalizing, intermediate mint/rotation is allowed again.
	require.NoError(t, runPkiRotateRoot(dir, "prd", "wardnet-mesh", true))
	require.NoError(t, runPkiRotateIntermediate(dir, "prd", "wardnet-mesh", "global"))
	require.NoError(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "us-east-1"))
}

func TestRunPkiRotateRootFinalizeRequiresRootIdentity(t *testing.T) {
	dir, rootIdentity, _ := mintedIntermediate(t)
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)
	require.NoError(t, runPkiRotateRoot(dir, "prd", "wardnet-mesh", false))

	// Finalize is custody-gated too: the wrong offline identity is rejected.
	wrong, _, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	t.Setenv(pki.RootIdentityEnvVar, wrong)
	require.ErrorContains(t, runPkiRotateRoot(dir, "prd", "wardnet-mesh", true), "decrypt current root key")
}

func TestRunPkiRotateRootRejectsRootOnly(t *testing.T) {
	dir, rootIdentity, _ := mintedIntermediate(t)
	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-daemon", pki.TopologyRootOnly, ""))
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)
	err := runPkiRotateRoot(dir, "prd", "wardnet-daemon", false)
	require.ErrorContains(t, err, "two-tier")
}

// verifyChain reports whether leaf chains to root through inter.
func verifyChain(leaf, inter, root *x509.Certificate) error {
	roots := x509.NewCertPool()
	roots.AddCert(root)
	inters := x509.NewCertPool()
	inters.AddCert(inter)
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err
}

func mustParse(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	c, err := pki.ParseCertificate(certPEM)
	require.NoError(t, err)
	return c
}
