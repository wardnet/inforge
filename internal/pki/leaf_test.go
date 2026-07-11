package pki_test

import (
	"crypto"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/pki"
)

func TestSPIFFEID(t *testing.T) {
	assert.Equal(t, "spiffe://wardnet.network/prd/us-east-1/bridge",
		pki.SPIFFEID("wardnet.network", "prd", "us-east-1", "bridge").String())
	assert.Equal(t, "spiffe://wardnet.network/prd/global/tenant",
		pki.SPIFFEID("wardnet.network", "prd", pki.ScopeGlobal, "tenant").String())
}

// meshChain mints a root + intermediate for leaf tests.
func meshChain(t *testing.T) (root, interCert *x509.Certificate, interSigner crypto.Signer) {
	t.Helper()
	rootCertPEM, rootKeyPEM, err := pki.GenerateRoot("wardnet-mesh root")
	require.NoError(t, err)
	rootCert, err := pki.ParseCertificate(rootCertPEM)
	require.NoError(t, err)
	rootSigner, err := pki.ParsePrivateKey(rootKeyPEM)
	require.NoError(t, err)
	icPEM, ikPEM, err := pki.GenerateIntermediate(rootCert, rootSigner, "wardnet-mesh us-east-1 intermediate")
	require.NoError(t, err)
	ic, err := pki.ParseCertificate(icPEM)
	require.NoError(t, err)
	ik, err := pki.ParsePrivateKey(ikPEM)
	require.NoError(t, err)
	return rootCert, ic, ik
}

func TestGenerateLeaf(t *testing.T) {
	root, interCert, interSigner := meshChain(t)

	spiffe := pki.SPIFFEID("wardnet.network", "prd", "us-east-1", "bridge")
	certPEM, keyPEM, err := pki.GenerateLeaf(interCert, interSigner, spiffe, "bridge")
	require.NoError(t, err)

	leaf, err := pki.ParseCertificate(certPEM)
	require.NoError(t, err)
	assert.False(t, leaf.IsCA, "leaf must not be a CA")
	assert.NotZero(t, leaf.KeyUsage&x509.KeyUsageDigitalSignature)
	assert.Contains(t, leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	assert.Contains(t, leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
	require.Len(t, leaf.URIs, 1)
	assert.Equal(t, spiffe.String(), leaf.URIs[0].String(), "scope is carried in the SPIFFE URI SAN")

	// Never outlives the intermediate; ~90d window.
	assert.False(t, leaf.NotAfter.After(interCert.NotAfter))
	d := time.Until(leaf.NotAfter)
	assert.Greater(t, d, 80*24*time.Hour)
	assert.Less(t, d, 100*24*time.Hour)

	// Chains to the intermediate, and the full leaf->intermediate->root verifies.
	require.NoError(t, leaf.CheckSignatureFrom(interCert))
	roots := x509.NewCertPool()
	roots.AddCert(root)
	inters := x509.NewCertPool()
	inters.AddCert(interCert)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	require.NoError(t, err)

	signer, err := pki.ParsePrivateKey(keyPEM)
	require.NoError(t, err)
	assert.Equal(t, leaf.PublicKey, signer.Public())
}

// TestGenerateLeafRejectsExpiredParent: clamping a leaf's NotAfter down to an
// already-expired parent would yield NotAfter <= NotBefore — a structurally
// invalid cert x509.CreateCertificate does NOT reject. GenerateLeaf must fail
// loudly instead so the operator knows to rotate/renew the intermediate first.
func TestGenerateLeafRejectsExpiredParent(t *testing.T) {
	_, interCert, interSigner := meshChain(t)
	expired := *interCert
	expired.NotAfter = time.Now().Add(-time.Hour)

	_, _, err := pki.GenerateLeaf(&expired, interSigner, pki.SPIFFEID("wardnet.network", "prd", "us-east-1", "bridge"), "bridge")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid into the future")
}
