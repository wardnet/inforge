// Package meshcert orchestrates mesh leaf minting at deploy/renew time: it
// decrypts a PKI's per-scope intermediate key, mints a short-TTL leaf from it,
// and computes the per-scope trust set a service verifies peers against. It is
// the deploy-side counterpart to the offline `inforge pki intermediate` flow —
// and the decrypt-side of the ADR-0024 custody rule: intermediate keys are
// encrypted to the CI recipient (pki.Store.IntermediateKeyRecipient), so they
// are unsealed here with the CI identity (INFORGE_SECRETS_KEY), never the cold
// offline root identity. It is the only place that pairs internal/pki with
// internal/secretstore so internal/pki stays free of age/secret concerns.
package meshcert

import (
	"crypto"
	"crypto/x509"
	"fmt"
	"sort"

	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/secretstore"
)

// Mesh cert delivery contract — shared by `inforge pki renew` (which writes the
// values to the provider) and the on-host descriptor's `files:` map (which
// references them so the bootstrapper projects them at boot, #109). The renew
// command writes CertFiles' keys as secrets under "<service path>/<MtlsDir>";
// DescriptorFiles points each *_PATH env var at the matching provider key.
const (
	MtlsDir = "mtls"

	leafCertFile = "leaf.crt"
	leafKeyFile  = "leaf.key"
	bundleFile   = "bundle.crt"

	EnvLeafCertPath    = "MTLS_LEAF_CERT_PATH"
	EnvLeafKeyPath     = "MTLS_LEAF_KEY_PATH"
	EnvTrustBundlePath = "MTLS_TRUST_BUNDLE_PATH"
)

// CertFiles maps the provider secret name → PEM value for a service's minted
// mesh material, written under the service's MtlsDir path by `inforge pki renew`.
func CertFiles(leafPEM, keyPEM, bundlePEM string) map[string]string {
	return map[string]string{leafCertFile: leafPEM, leafKeyFile: keyPEM, bundleFile: bundlePEM}
}

// DescriptorFiles is the descriptor `files:` map for a mesh service: each *_PATH
// env var → its provider key relative to the service secret path. The host
// bootstrapper fetches the key, writes the PEM to a tmpfs file, and sets the env
// var to that path (#109).
func DescriptorFiles() map[string]string {
	return map[string]string{
		EnvLeafCertPath:    MtlsDir + "/" + leafCertFile,
		EnvLeafKeyPath:     MtlsDir + "/" + leafKeyFile,
		EnvTrustBundlePath: MtlsDir + "/" + bundleFile,
	}
}

// MintServiceLeaf mints a leaf for service under ownScope of the named two-tier
// PKI: it unseals that scope's intermediate key with ciIdentity (the CI age
// identity), then signs a leaf carrying the SPIFFE identity
// spiffe://<trustDomain>/<env>/<ownScope>/<service>. Returns the leaf cert and
// key as PEM.
func MintServiceLeaf(store *pki.Store, pkiName, ownScope, ciIdentity, trustDomain, env, service string) (leafPEM, keyPEM string, err error) {
	interCert, interKey, err := IntermediateSigner(store, pkiName, ownScope, ciIdentity)
	if err != nil {
		return "", "", err
	}
	return MintLeaf(interCert, interKey, trustDomain, env, ownScope, service)
}

// IntermediateSigner decrypts and parses a two-tier PKI's scope intermediate
// (cert + key) with ciIdentity (the CI age identity — the decrypt-side of the
// custody rule; intermediate keys are encrypted to the CI recipient, never the
// cold root). Callers minting several leaves from the same scope cache the
// returned pair per (pkiName, scope) to decrypt once.
func IntermediateSigner(store *pki.Store, pkiName, scope, ciIdentity string) (*x509.Certificate, crypto.Signer, error) {
	p, ok := store.Get(pkiName)
	if !ok {
		return nil, nil, fmt.Errorf("pki %q not found", pkiName)
	}
	if p.Topology != pki.TopologyTwoTier {
		return nil, nil, fmt.Errorf("pki %q is %s; mesh leaves require a two-tier PKI", pkiName, p.Topology)
	}
	inter, ok := p.Intermediates[scope]
	if !ok {
		return nil, nil, fmt.Errorf("pki %q has no intermediate for scope %q — run `inforge pki intermediate <env> %s %s`", pkiName, scope, pkiName, scope)
	}
	keyPlain, err := secretstore.Decrypt(inter.Key, ciIdentity)
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt intermediate key for pki %q scope %q: %w", pkiName, scope, err)
	}
	interCert, err := pki.ParseCertificate(inter.Cert)
	if err != nil {
		return nil, nil, err
	}
	interKey, err := pki.ParsePrivateKey(string(keyPlain))
	if err != nil {
		return nil, nil, err
	}
	return interCert, interKey, nil
}

// MintLeaf mints a leaf for service from an already-decrypted scope intermediate
// (see IntermediateSigner), carrying the spiffe://<trustDomain>/<env>/<scope>/
// <service> identity.
func MintLeaf(interCert *x509.Certificate, interKey crypto.Signer, trustDomain, env, scope, service string) (leafPEM, keyPEM string, err error) {
	return pki.GenerateLeaf(interCert, interKey, pki.SPIFFEID(trustDomain, env, scope, service), service)
}

// TrustSet returns the scopes a service in ownScope verifies peers against
// (ADR-0024 regional boundary): a global service trusts every region plus
// global; a regional service trusts its own region plus global. The result is
// sorted and deduplicated, suitable for pki.Store.TrustBundle.
func TrustSet(ownScope string, allRegions []string) []string {
	set := map[string]bool{pki.ScopeGlobal: true}
	if ownScope == pki.ScopeGlobal {
		for _, r := range allRegions {
			set[r] = true
		}
	} else {
		set[ownScope] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
