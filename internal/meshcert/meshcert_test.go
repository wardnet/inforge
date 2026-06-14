package meshcert_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/meshcert"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/secretstore"
)

func TestTrustSet(t *testing.T) {
	regions := []string{"us-east-1", "eu-central-1"}
	// A global service trusts every region plus global.
	assert.Equal(t, []string{"eu-central-1", "global", "us-east-1"},
		meshcert.TrustSet(pki.ScopeGlobal, regions))
	// A regional service trusts its own region plus global.
	assert.Equal(t, []string{"global", "us-east-1"},
		meshcert.TrustSet("us-east-1", regions))
}

// meshStore builds a two-tier mesh whose us-east-1 intermediate key is encrypted
// to ciRecipient (the CI recipient), as deploy/renew would find it.
func meshStore(t *testing.T, ciRecipient string) *pki.Store {
	t.Helper()
	rootCertPEM, rootKeyPEM, err := pki.GenerateRoot("wardnet-mesh root")
	require.NoError(t, err)
	rootCert, err := pki.ParseCertificate(rootCertPEM)
	require.NoError(t, err)
	rootSigner, err := pki.ParsePrivateKey(rootKeyPEM)
	require.NoError(t, err)
	icPEM, ikPEM, err := pki.GenerateIntermediate(rootCert, rootSigner, "wardnet-mesh us-east-1 intermediate")
	require.NoError(t, err)
	ct, err := secretstore.Encrypt([]byte(ikPEM), ciRecipient)
	require.NoError(t, err)

	s := &pki.Store{RootRecipient: "age1root", Recipient: ciRecipient}
	s.Set("wardnet-mesh", pki.PKI{
		Topology:      pki.TopologyTwoTier,
		Root:          pki.Material{Cert: rootCertPEM, Key: "cold"},
		Intermediates: map[string]pki.Material{"us-east-1": {Cert: icPEM, Key: ct}},
	})
	s.Set("wardnet-daemon", pki.PKI{Topology: pki.TopologyRootOnly, Scope: "global"})
	return s
}

func TestMintServiceLeaf(t *testing.T) {
	ciIdentity, ciRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	store := meshStore(t, ciRecipient)

	leafPEM, keyPEM, err := meshcert.MintServiceLeaf(store, "wardnet-mesh", "us-east-1", ciIdentity, "wardnet.network", "prd", "bridge")
	require.NoError(t, err)
	assert.NotEmpty(t, keyPEM)

	leaf, err := pki.ParseCertificate(leafPEM)
	require.NoError(t, err)
	require.Len(t, leaf.URIs, 1)
	assert.Equal(t, "spiffe://wardnet.network/prd/us-east-1/bridge", leaf.URIs[0].String())

	// The leaf chains to the mesh's us-east-1 intermediate.
	interCert, err := pki.ParseCertificate(store.PKIs["wardnet-mesh"].Intermediates["us-east-1"].Cert)
	require.NoError(t, err)
	require.NoError(t, leaf.CheckSignatureFrom(interCert))
}

func TestMintServiceLeafErrors(t *testing.T) {
	ciIdentity, ciRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	store := meshStore(t, ciRecipient)

	// Wrong identity cannot decrypt the intermediate key.
	wrong, _, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	_, _, err = meshcert.MintServiceLeaf(store, "wardnet-mesh", "us-east-1", wrong, "wardnet.network", "prd", "bridge")
	require.ErrorContains(t, err, "decrypt intermediate key")

	// Root-only PKI is not a mesh.
	_, _, err = meshcert.MintServiceLeaf(store, "wardnet-daemon", "global", ciIdentity, "wardnet.network", "prd", "bridge")
	require.ErrorContains(t, err, "two-tier")

	// Missing scope intermediate.
	_, _, err = meshcert.MintServiceLeaf(store, "wardnet-mesh", "eu-central-1", ciIdentity, "wardnet.network", "prd", "bridge")
	require.ErrorContains(t, err, "no intermediate for scope")

	// Unknown PKI.
	_, _, err = meshcert.MintServiceLeaf(store, "nope", "global", ciIdentity, "wardnet.network", "prd", "bridge")
	require.ErrorContains(t, err, "not found")
}
