package secretstore_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/secretstore"
)

func TestPath(t *testing.T) {
	assert.Equal(t, filepath.Join("resources", "prd", "secrets.enc.yaml"),
		secretstore.Path("resources", "prd"))
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	s := &secretstore.Store{Recipient: "age1test"}
	s.Set("ghost", "API_KEY", "-----BEGIN AGE ENCRYPTED FILE-----\nabc\n-----END AGE ENCRYPTED FILE-----")
	s.Set("ghost", "SESSION_KEY", "ct2")
	s.Set("bridge", "TOKEN", "ct3")
	require.NoError(t, s.Save(path))

	got, err := secretstore.Load(path)
	require.NoError(t, err)
	assert.Equal(t, s.Recipient, got.Recipient)
	assert.Equal(t, s.Services, got.Services)

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(b), "# managed by `inforge secret`"),
		"saved store must carry the do-not-edit header")
}

// TestSaveDeterministic locks in byte-identical output for identical content,
// so a one-key rotation diffs as a single-entry change in git.
func TestSaveDeterministic(t *testing.T) {
	dir := t.TempDir()
	build := func() *secretstore.Store {
		s := &secretstore.Store{Recipient: "age1test"}
		// Insert in different orders; output must not depend on it.
		s.Set("zeta", "B_KEY", "ct1")
		s.Set("alpha", "Z_KEY", "ct2")
		s.Set("alpha", "A_KEY", "ct3")
		return s
	}
	p1, p2 := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	require.NoError(t, build().Save(p1))
	require.NoError(t, build().Save(p2))

	b1, err := os.ReadFile(p1)
	require.NoError(t, err)
	b2, err := os.ReadFile(p2)
	require.NoError(t, err)
	assert.Equal(t, string(b1), string(b2))
}

func TestLoadMissingFileIsErrNotFound(t *testing.T) {
	_, err := secretstore.Load(filepath.Join(t.TempDir(), "secrets.enc.yaml"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, secretstore.ErrNotFound))
}

func TestLoadCorruptYAMLFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	require.NoError(t, os.WriteFile(path, []byte("recipient: [unclosed"), 0o644))
	_, err := secretstore.Load(path)
	require.Error(t, err)
	assert.False(t, errors.Is(err, secretstore.ErrNotFound))
}

func TestLoadMissingRecipientFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	require.NoError(t, os.WriteFile(path, []byte("containers:\n  ghost:\n    K: ct\n"), 0o644))
	_, err := secretstore.Load(path)
	require.ErrorContains(t, err, "no recipient")
}

// TestLoadUnmigratedContainersFails: a store still carrying the pre-ADR-0040
// container-keyed block is REJECTED, not silently ignored. yaml.v3 decodes
// non-strictly, so without the detect-only Containers field the block would be
// dropped and the store would load clean while serving no secrets at all —
// every service would come up missing every value. Load is the single guard, so
// validate, the CLI and the deploy all inherit it.
func TestLoadUnmigratedContainersFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	require.NoError(t, os.WriteFile(path, []byte("recipient: age1test\ncontainers:\n  edge:\n    OTEL_AUTH_TOKEN: ct\n"), 0o644))

	_, err := secretstore.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "`containers:`", "the error must name the stale block")
	assert.Contains(t, err.Error(), "edge", "and list the containers to migrate")
	assert.Contains(t, err.Error(), "services:", "and say where the entries belong")
	assert.False(t, errors.Is(err, secretstore.ErrNotFound), "an unmigrated store exists — it is not a missing one")
}

// TestSaveNeverWritesContainers: the legacy field is detect-only. A store that
// loaded clean and is written back must not resurrect a `containers:` key.
func TestSaveNeverWritesContainers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	s := &secretstore.Store{Recipient: "age1test"}
	s.Set("api", "TOKEN", "ct")
	require.NoError(t, s.Save(path))

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "containers:")
	assert.Contains(t, string(b), "services:")
}

func TestGetSetDeleteKeys(t *testing.T) {
	s := &secretstore.Store{Recipient: "age1test"}

	_, ok := s.Get("ghost", "K")
	assert.False(t, ok)
	assert.Empty(t, s.Keys("ghost"))

	s.Set("ghost", "B", "ct-b")
	s.Set("ghost", "A", "ct-a")
	v, ok := s.Get("ghost", "A")
	assert.True(t, ok)
	assert.Equal(t, "ct-a", v)
	assert.Equal(t, []string{"A", "B"}, s.Keys("ghost"))

	assert.False(t, s.Delete("ghost", "MISSING"))
	assert.False(t, s.Delete("other", "A"))
	assert.True(t, s.Delete("ghost", "A"))
	assert.True(t, s.Delete("ghost", "B"))
	// Emptied service map is pruned so the saved YAML carries no husk.
	assert.NotContains(t, s.Services, "ghost")
}

// TestReservedNamespaceIsolated: reserved secrets share none of the service
// namespace — a reserved namespace and a SERVICE of the same name coexist
// without collision (ADR-0034 / the observability fix), and the two-level-map
// behaviour (nil-alloc, prune, sort) matches the service accessors.
func TestReservedNamespaceIsolated(t *testing.T) {
	s := &secretstore.Store{Recipient: "age1test"}

	// A service and a reserved namespace with the same name do not collide.
	s.Set("observability", "SVC_TOKEN", "ct-svc")
	s.SetReserved("observability", "otlp_auth", "ct-auth")

	if _, ok := s.Get("observability", "otlp_auth"); ok {
		t.Error("reserved key leaked into the service namespace")
	}
	if _, ok := s.GetReserved("observability", "SVC_TOKEN"); ok {
		t.Error("service key leaked into the reserved namespace")
	}
	v, ok := s.GetReserved("observability", "otlp_auth")
	assert.True(t, ok)
	assert.Equal(t, "ct-auth", v)
	assert.Equal(t, []string{"SVC_TOKEN"}, s.Keys("observability"))
	assert.Equal(t, []string{"otlp_auth"}, s.ReservedKeys("observability"))

	// Prune behaviour mirrors the service map.
	assert.False(t, s.DeleteReserved("observability", "missing"))
	assert.True(t, s.DeleteReserved("observability", "otlp_auth"))
	assert.NotContains(t, s.Reserved, "observability")
	// The same-named service is untouched by the reserved delete.
	assert.Equal(t, []string{"SVC_TOKEN"}, s.Keys("observability"))
}

// The store is read through the one reader now. A corrupt file must still fail with an
// error that NAMES the subsystem — an operator hitting this at 3am needs to know it is
// the secret store, not just "some yaml".
func TestLoadMalformedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	require.NoError(t, os.WriteFile(path, []byte("recipient: [unclosed\n"), 0o600))

	_, err := secretstore.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secrets.enc.yaml")
}

// A structurally valid document whose types do not match the store fails at decode.
func TestLoadStoreTypeMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc.yaml")
	require.NoError(t, os.WriteFile(path, []byte("recipient:\n  - a list, not a string\n"), 0o600))

	_, err := secretstore.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse secret store")
}
