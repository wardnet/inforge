package pki_test

import (
	"crypto/ed25519"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/pki"
)

func TestGenerateRoot(t *testing.T) {
	certPEM, keyPEM, err := pki.GenerateRoot("wardnet-mesh root")
	require.NoError(t, err)

	cert, err := pki.ParseCertificate(certPEM)
	require.NoError(t, err)

	assert.True(t, cert.IsCA, "root must be a CA")
	assert.True(t, cert.BasicConstraintsValid)
	assert.NotZero(t, cert.KeyUsage&x509.KeyUsageCertSign, "root must be able to sign certs")
	assert.Equal(t, "wardnet-mesh root", cert.Subject.CommonName)
	assert.IsType(t, ed25519.PublicKey{}, cert.PublicKey, "key algorithm is ed25519")

	// Validity is the long-lived root window (~10y), within a clock-skew margin.
	d := time.Until(cert.NotAfter)
	assert.Greater(t, d, 9*365*24*time.Hour)
	assert.Less(t, d, 11*365*24*time.Hour)

	// The key round-trips and matches the certificate's public key.
	signer, err := pki.ParsePrivateKey(keyPEM)
	require.NoError(t, err)
	assert.Equal(t, cert.PublicKey, signer.Public())

	// A self-signed root verifies its own signature.
	require.NoError(t, cert.CheckSignatureFrom(cert))
}

func TestGenerateIntermediate(t *testing.T) {
	rootCertPEM, rootKeyPEM, err := pki.GenerateRoot("wardnet-mesh root")
	require.NoError(t, err)
	rootCert, err := pki.ParseCertificate(rootCertPEM)
	require.NoError(t, err)
	rootSigner, err := pki.ParsePrivateKey(rootKeyPEM)
	require.NoError(t, err)

	certPEM, keyPEM, err := pki.GenerateIntermediate(rootCert, rootSigner, "wardnet-mesh global intermediate")
	require.NoError(t, err)

	cert, err := pki.ParseCertificate(certPEM)
	require.NoError(t, err)
	assert.True(t, cert.IsCA, "intermediate must be a CA")
	assert.True(t, cert.BasicConstraintsValid)
	assert.NotZero(t, cert.KeyUsage&x509.KeyUsageCertSign, "intermediate must sign leaves")
	// Path length zero: signs leaves only, never further sub-CAs.
	assert.True(t, cert.MaxPathLenZero)
	assert.Equal(t, 0, cert.MaxPathLen)
	assert.Equal(t, "wardnet-mesh global intermediate", cert.Subject.CommonName)
	assert.IsType(t, ed25519.PublicKey{}, cert.PublicKey)

	// The intermediate chains to the root (signed by the root key)...
	require.NoError(t, cert.CheckSignatureFrom(rootCert))
	// ...and a full chain verify succeeds.
	roots := x509.NewCertPool()
	roots.AddCert(rootCert)
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	require.NoError(t, err)

	// It never outlives the root.
	assert.False(t, cert.NotAfter.After(rootCert.NotAfter))

	// Its key round-trips and matches its own certificate.
	signer, err := pki.ParsePrivateKey(keyPEM)
	require.NoError(t, err)
	assert.Equal(t, cert.PublicKey, signer.Public())
}

func TestRootIdentityFromEnv(t *testing.T) {
	t.Setenv(pki.RootIdentityEnvVar, "  ")
	_, err := pki.RootIdentityFromEnv()
	require.ErrorContains(t, err, pki.RootIdentityEnvVar)

	t.Setenv(pki.RootIdentityEnvVar, "  AGE-SECRET-KEY-XYZ  ")
	id, err := pki.RootIdentityFromEnv()
	require.NoError(t, err)
	assert.Equal(t, "AGE-SECRET-KEY-XYZ", id, "value must be trimmed")
}

func TestGenerateRootUniqueSerials(t *testing.T) {
	c1, _, err := pki.GenerateRoot("a")
	require.NoError(t, err)
	c2, _, err := pki.GenerateRoot("b")
	require.NoError(t, err)
	cert1, err := pki.ParseCertificate(c1)
	require.NoError(t, err)
	cert2, err := pki.ParseCertificate(c2)
	require.NoError(t, err)
	assert.NotEqual(t, cert1.SerialNumber, cert2.SerialNumber)
}

func TestParseCertificateRejectsJunk(t *testing.T) {
	_, err := pki.ParseCertificate("not a pem")
	require.Error(t, err)

	// A PEM block of the wrong type is rejected.
	_, keyPEM, err := pki.GenerateRoot("x")
	require.NoError(t, err)
	_, err = pki.ParseCertificate(keyPEM)
	require.Error(t, err)
}

func TestParsePrivateKeyRejectsJunk(t *testing.T) {
	_, err := pki.ParsePrivateKey("not a pem")
	require.Error(t, err)

	certPEM, _, err := pki.GenerateRoot("x")
	require.NoError(t, err)
	_, err = pki.ParsePrivateKey(certPEM)
	require.Error(t, err)
}
