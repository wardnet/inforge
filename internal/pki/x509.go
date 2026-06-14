// Package pki implements the git-native PKI store of ADR-0024: the certificate
// hierarchies that secure the Wardnet service mesh and the daemon trust tree
// live in the consumer repo at resources/<env>/pki.enc.yaml, the structural
// twin of the ADR-0017 secret store. Certificates are committed as plaintext
// PEM (they are public); private keys are committed as age ciphertext. A
// two-tier PKI's cold root key is encrypted to an offline operator recipient
// that CI never holds, so CI literally cannot sign with it; a root-only PKI's
// key is encrypted to the CI recipient (INFORGE_SECRETS_KEY) for delivery to a
// designated online issuer at deploy.
//
// This file holds the pure-Go x509 primitives. All inforge binaries are
// CGO_ENABLED=0 static, so key and certificate handling uses only stdlib
// crypto — no openssl shell-out, no cgo.
package pki

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"
)

// rootValidity is how long a generated root CA is valid. Roots are long-lived
// anchors (intermediates and leaves, minted in later slices, are far shorter);
// rotation is a deliberate, operator-driven event (ADR-0024, slice #110).
const rootValidity = 10 * 365 * 24 * time.Hour

// intermediateValidity is how long a minted intermediate CA is valid. It sits
// below the root (which it never outlives — GenerateIntermediate clamps to the
// parent) and above the short-TTL leaves that deploy mints from it (#108).
const intermediateValidity = 5 * 365 * 24 * time.Hour

// leafValidity is how long a minted mesh leaf is valid. Leaves are re-minted by
// `inforge pki renew` (non-disruptively — a running service keeps its in-memory
// cert until the host re-projects, #109), so the TTL only needs to comfortably
// exceed the renew/reboot interval; 90 days gives a wide safety margin.
const leafValidity = 90 * 24 * time.Hour

// RootIdentityEnvVar names the environment variable that carries the offline
// operator identity (AGE-SECRET-KEY-…) matching a two-tier PKI's rootRecipient.
// `inforge pki init` prints that identity once; slice #106 reads it from here
// to sign per-scope intermediates with the cold root. It is deliberately
// distinct from secretstore.IdentityEnvVar (INFORGE_SECRETS_KEY): the cold root
// key must never be reachable by the CI master.
const RootIdentityEnvVar = "INFORGE_PKI_ROOT_KEY"

// RootIdentityFromEnv reads the offline root identity from RootIdentityEnvVar,
// with an actionable error when it is unset. It mirrors
// secretstore.IdentityFromEnv but is deliberately separate: this identity
// unseals cold two-tier roots to mint intermediates and must never be the CI
// master (INFORGE_SECRETS_KEY).
func RootIdentityFromEnv() (string, error) {
	id := strings.TrimSpace(os.Getenv(RootIdentityEnvVar))
	if id == "" {
		return "", fmt.Errorf("%s is unset — set it to the offline root identity (AGE-SECRET-KEY-…) printed by `inforge pki init`", RootIdentityEnvVar)
	}
	return id, nil
}

const (
	pemTypeCertificate = "CERTIFICATE"
	pemTypePrivateKey  = "PRIVATE KEY"
)

// newCAKey mints a fresh CA key pair. It is the single seam for the key
// algorithm used across the whole hierarchy — roots here, intermediates and
// leaves in later slices. Ed25519 (ADR-0024): the simplest primitive, with no
// per-signature nonce risk, fully supported by Go's crypto/x509 and by the
// rustls/webpki consumers in the fully-controlled mesh. Switching the entire
// hierarchy to another algorithm is a change confined to this function.
func newCAKey() (crypto.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return priv, nil
}

// GenerateRoot mints a self-signed root CA and returns its certificate as
// CERTIFICATE PEM (public, committed verbatim) and its private key as PKCS#8
// PRIVATE KEY PEM (the caller age-encrypts this before it touches disk). The
// root is a CA with cert/CRL signing usage; its path length is left
// unconstrained, since whether a leaf sits directly below it (root-only) or
// below an intermediate (two-tier) is decided by later slices.
func GenerateRoot(commonName string) (certPEM, keyPEM string, err error) {
	signer, err := newCAKey()
	if err != nil {
		return "", "", err
	}
	serial, err := randomSerial()
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute), // tolerate minor clock skew at verifiers
		NotAfter:              now.Add(rootValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, signer.Public(), signer)
	if err != nil {
		return "", "", fmt.Errorf("create root certificate: %w", err)
	}
	return encodeCertAndKey(der, signer)
}

// GenerateIntermediate mints an intermediate CA signed by parent (the root
// cert) using parentKey (the root's private key, decrypted from the offline
// rootRecipient by the operator). It returns the intermediate certificate as
// CERTIFICATE PEM and its private key as PKCS#8 PRIVATE KEY PEM (the caller
// age-encrypts the key to the CI recipient). The intermediate is capped at path
// length zero (MaxPathLenZero) — it signs only leaves, never further sub-CAs —
// and never outlives the parent (NotAfter is clamped to the parent's).
func GenerateIntermediate(parent *x509.Certificate, parentKey crypto.Signer, commonName string) (certPEM, keyPEM string, err error) {
	signer, err := newCAKey()
	if err != nil {
		return "", "", err
	}
	serial, err := randomSerial()
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	notAfter := now.Add(intermediateValidity)
	if notAfter.After(parent.NotAfter) {
		notAfter = parent.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute), // tolerate minor clock skew at verifiers
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true, // signs leaves only — no further sub-CAs
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, signer.Public(), parentKey)
	if err != nil {
		return "", "", fmt.Errorf("create intermediate certificate: %w", err)
	}
	return encodeCertAndKey(der, signer)
}

// encodeCertAndKey renders a signed DER certificate and its signer's private
// key as the CERTIFICATE / PKCS#8 PRIVATE KEY PEM pair the store commits. Both
// GenerateRoot and GenerateIntermediate end here, so the key marshaling and PEM
// block types stay defined in one place.
func encodeCertAndKey(der []byte, signer crypto.Signer) (certPEM, keyPEM string, err error) {
	keyDER, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return "", "", fmt.Errorf("marshal CA key: %w", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: pemTypePrivateKey, Bytes: keyDER}))
	return certPEM, keyPEM, nil
}

// ParseCertificate decodes a single CERTIFICATE PEM block.
func ParseCertificate(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != pemTypeCertificate {
		return nil, fmt.Errorf("no %s PEM block found", pemTypeCertificate)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return cert, nil
}

// ParsePrivateKey decodes a PKCS#8 PRIVATE KEY PEM block and returns it as a
// crypto.Signer (every key algorithm this package mints satisfies it).
func ParsePrivateKey(keyPEM string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil || block.Type != pemTypePrivateKey {
		return nil, fmt.Errorf("no %s PEM block found", pemTypePrivateKey)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("private key of type %T does not implement crypto.Signer", key)
	}
	return signer, nil
}

// randomSerial draws a 128-bit positive certificate serial number, as the Web
// PKI requires for collision resistance.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}
