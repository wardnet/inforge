package pki_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/pki"
)

func TestPath(t *testing.T) {
	assert.Equal(t, filepath.Join("resources", "prd", "pki.enc.yaml"),
		pki.Path("resources", "prd"))
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pki.enc.yaml")
	s := &pki.Store{RootRecipient: "age1root", Recipient: "age1ci"}
	s.Set("wardnet-mesh", pki.PKI{
		Topology: pki.TopologyTwoTier,
		Root:     pki.Material{Cert: "CERT-PEM", Key: "AGE-CT"},
		Intermediates: map[string]pki.Material{
			"global": {Cert: "I-CERT", Key: "I-CT"},
		},
	})
	s.Set("wardnet-daemon", pki.PKI{
		Topology: pki.TopologyRootOnly,
		Scope:    "global",
		Root:     pki.Material{Cert: "D-CERT", Key: "D-CT"},
	})
	require.NoError(t, s.Save(path))

	got, err := pki.Load(path)
	require.NoError(t, err)
	assert.Equal(t, s.RootRecipient, got.RootRecipient)
	assert.Equal(t, s.Recipient, got.Recipient)
	assert.Equal(t, s.PKIs, got.PKIs)

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(b), "# managed by `inforge pki`"),
		"saved store must carry the do-not-edit header")
}

func TestLoadMissingIsErrNotFound(t *testing.T) {
	_, err := pki.Load(filepath.Join(t.TempDir(), "pki.enc.yaml"))
	assert.True(t, errors.Is(err, pki.ErrNotFound))
}

func TestLoadMissingRecipientIsCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pki.enc.yaml")
	// rootRecipient present but no recipient — nothing could have encrypted a
	// root-only key, so the store is corrupt.
	require.NoError(t, os.WriteFile(path, []byte("rootRecipient: age1root\n"), 0o644))
	_, err := pki.Load(path)
	require.ErrorContains(t, err, "missing a recipient")
}

func TestLoadRejectsUnknownTopology(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pki.enc.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"rootRecipient: age1root\nrecipient: age1ci\npkis:\n  x:\n    topology: single-tier\n    root:\n      cert: C\n      key: K\n"), 0o644))
	_, err := pki.Load(path)
	require.ErrorContains(t, err, "unknown topology")
}

func TestValidTopology(t *testing.T) {
	assert.True(t, pki.ValidTopology(pki.TopologyTwoTier))
	assert.True(t, pki.ValidTopology(pki.TopologyRootOnly))
	assert.False(t, pki.ValidTopology(""))
	assert.False(t, pki.ValidTopology("single-tier"))
}

func TestTrustBundle(t *testing.T) {
	s := &pki.Store{RootRecipient: "age1root", Recipient: "age1ci"}
	s.Set("wardnet-mesh", pki.PKI{
		Topology: pki.TopologyTwoTier,
		Root:     pki.Material{Cert: "ROOT-CERT", Key: "x"},
		Intermediates: map[string]pki.Material{
			"global":    {Cert: "GLOBAL-CERT", Key: "x"},
			"us-east-1": {Cert: "USE1-CERT", Key: "x"},
		},
	})

	// The scoped intermediates come first, then the root — nginx/OpenSSL needs a
	// self-signed anchor to build the chain, so the root MUST be present or every
	// mesh handshake fails.
	bundle, err := s.TrustBundle("wardnet-mesh", []string{"global", "us-east-1"})
	require.NoError(t, err)
	assert.Equal(t, "GLOBAL-CERT\nUSE1-CERT\nROOT-CERT\n", bundle)

	_, err = s.TrustBundle("wardnet-mesh", []string{"eu-central-1"})
	require.ErrorContains(t, err, `no intermediate for scope "eu-central-1"`)

	_, err = s.TrustBundle("absent", []string{"global"})
	require.ErrorContains(t, err, "not found")
}

// TestTrustBundleRejectsMissingRoot: a two-tier PKI with intermediates but no
// root cert would yield an anchor-less bundle nginx/OpenSSL can never build a
// chain to — the exact defect the root was added to fix — so TrustBundle must
// error rather than ship it silently.
func TestTrustBundleRejectsMissingRoot(t *testing.T) {
	s := &pki.Store{RootRecipient: "age1root", Recipient: "age1ci"}
	s.Set("wardnet-mesh", pki.PKI{
		Topology:      pki.TopologyTwoTier,
		Intermediates: map[string]pki.Material{"global": {Cert: "GLOBAL-CERT", Key: "x"}},
	})

	_, err := s.TrustBundle("wardnet-mesh", []string{"global"})
	require.ErrorContains(t, err, "no root certificate")
}

// TestTrustBundleIncludesPreviousRoots: mid dual-root overlap the bundle must
// carry BOTH the active and the retained previous root, so a leaf signed under
// either root still verifies during a root rotation.
func TestTrustBundleIncludesPreviousRoots(t *testing.T) {
	s := &pki.Store{RootRecipient: "age1root", Recipient: "age1ci"}
	s.Set("wardnet-mesh", pki.PKI{
		Topology:      pki.TopologyTwoTier,
		Root:          pki.Material{Cert: "NEW-ROOT", Key: "x"},
		PreviousRoots: []pki.Material{{Cert: "OLD-ROOT", Key: "x"}},
		Intermediates: map[string]pki.Material{"global": {Cert: "GLOBAL-CERT", Key: "x"}},
	})

	bundle, err := s.TrustBundle("wardnet-mesh", []string{"global"})
	require.NoError(t, err)
	assert.Equal(t, "GLOBAL-CERT\nNEW-ROOT\nOLD-ROOT\n", bundle)
}

func TestRootCerts(t *testing.T) {
	// Outside an overlap, RootCerts is just the active root.
	p := pki.PKI{Topology: pki.TopologyTwoTier, Root: pki.Material{Cert: "NEW-ROOT"}}
	assert.Equal(t, []string{"NEW-ROOT"}, p.RootCerts())

	// Mid-overlap, it is the active root plus every retained previous root.
	p.PreviousRoots = []pki.Material{{Cert: "OLD-ROOT"}}
	assert.Equal(t, []string{"NEW-ROOT", "OLD-ROOT"}, p.RootCerts())
}

func TestOverlapFieldsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pki.enc.yaml")
	s := &pki.Store{RootRecipient: "age1root", Recipient: "age1ci"}
	s.Set("wardnet-mesh", pki.PKI{
		Topology:      pki.TopologyTwoTier,
		Root:          pki.Material{Cert: "NEW-ROOT", Key: "NEW-CT"},
		PreviousRoots: []pki.Material{{Cert: "OLD-ROOT", Key: "OLD-CT"}},
		Intermediates: map[string]pki.Material{
			"global": {Cert: "NEW-GLOBAL-INTER", Key: "GLOBAL-CT"},
		},
		PreviousIntermediates: map[string][]string{
			"global": {"OLD-GLOBAL-INTER"},
		},
	})
	require.NoError(t, s.Save(path))

	got, err := pki.Load(path)
	require.NoError(t, err)
	assert.Equal(t, s.PKIs, got.PKIs, "overlap fields must survive a save/load round-trip")
}

func TestNamesGetSet(t *testing.T) {
	s := &pki.Store{RootRecipient: "age1root", Recipient: "age1ci"}
	assert.Empty(t, s.Names())

	_, ok := s.Get("absent")
	assert.False(t, ok)

	s.Set("b", pki.PKI{Topology: pki.TopologyRootOnly, Scope: "global"})
	s.Set("a", pki.PKI{Topology: pki.TopologyTwoTier})
	assert.Equal(t, []string{"a", "b"}, s.Names())

	p, ok := s.Get("b")
	assert.True(t, ok)
	assert.Equal(t, pki.TopologyRootOnly, p.Topology)
}

// Read through the one reader; a corrupt store still names itself.
func TestLoadMalformedPkiStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pki.enc.yaml")
	require.NoError(t, os.WriteFile(path, []byte("recipient: [unclosed\n"), 0o600))

	_, err := pki.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pki.enc.yaml")
}

func TestLoadPkiStoreTypeMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pki.enc.yaml")
	require.NoError(t, os.WriteFile(path, []byte("recipient:\n  - not a string\n"), 0o600))

	_, err := pki.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse pki store")
}
