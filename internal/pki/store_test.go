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
