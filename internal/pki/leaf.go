package pki

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"net/url"
	"time"
)

// SPIFFEID builds the workload identity a mesh leaf carries in its URI SAN:
// spiffe://<trustDomain>/<env>/<scope>/<service>. trustDomain is the env's base
// domain; scope is ScopeGlobal or an abstract region name. The acceptor reads
// the scope path segment to enforce the regional boundary (ADR-0024 amendment).
// It returns a *url.URL so GenerateLeaf consumes it directly (no string
// round-trip); call .String() for the canonical form.
func SPIFFEID(trustDomain, env, scope, service string) *url.URL {
	return &url.URL{Scheme: "spiffe", Host: trustDomain, Path: "/" + env + "/" + scope + "/" + service}
}

// GenerateLeaf mints a short-TTL mTLS leaf signed by parent (a scope's
// intermediate) using parentKey (decrypted from the CI recipient at deploy). The
// leaf is a non-CA end-entity good for both client and server auth — mesh
// services both initiate and accept — carrying spiffeID as its URI SAN so a peer
// can authorize on the encoded scope. It never outlives the parent (NotAfter is
// clamped). Returns the leaf as CERTIFICATE PEM and its key as PKCS#8 PRIVATE KEY
// PEM (the caller writes the key to the secrets provider).
func GenerateLeaf(parent *x509.Certificate, parentKey crypto.Signer, spiffeID *url.URL, commonName string, dnsNames ...string) (certPEM, keyPEM string, err error) {
	signer, err := newCAKey()
	if err != nil {
		return "", "", err
	}
	serial, err := randomSerial()
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	notAfter, err := clampToParent(now, leafValidity, parent)
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute), // tolerate minor clock skew at verifiers
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		URIs:                  []*url.URL{spiffeID},
		DNSNames:              dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, signer.Public(), parentKey)
	if err != nil {
		return "", "", fmt.Errorf("create leaf certificate: %w", err)
	}
	return encodeCertAndKey(der, signer)
}
