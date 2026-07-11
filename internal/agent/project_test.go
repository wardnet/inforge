package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"MTLS_LEAF_CERT_PATH": "mtls/leaf.crt",
		"MTLS_LEAF_KEY_PATH":  "mtls/leaf.key",
	}
	secrets := map[string]string{
		"mtls/leaf.crt": "CERT",
		"mtls/leaf.key": "KEY",
	}

	pathEnv, changed, err := projectFiles(files, secrets, dir, os.Getuid(), os.Getgid())
	require.NoError(t, err)
	assert.True(t, changed, "first projection writes the files")
	assert.ElementsMatch(t, []string{
		"MTLS_LEAF_CERT_PATH=" + filepath.Join(dir, "mtls/leaf.crt"),
		"MTLS_LEAF_KEY_PATH=" + filepath.Join(dir, "mtls/leaf.key"),
	}, pathEnv)

	b, err := os.ReadFile(filepath.Join(dir, "mtls/leaf.crt"))
	require.NoError(t, err)
	assert.Equal(t, "CERT", string(b))
	info, err := os.Stat(filepath.Join(dir, "mtls/leaf.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o400), info.Mode().Perm(), "leaf key is 0400")

	// Re-projecting identical content reports no change (renewal would not reload).
	_, changed, err = projectFiles(files, secrets, dir, os.Getuid(), os.Getgid())
	require.NoError(t, err)
	assert.False(t, changed)

	// New content reports a change and is written.
	secrets["mtls/leaf.crt"] = "CERT2"
	_, changed, err = projectFiles(files, secrets, dir, os.Getuid(), os.Getgid())
	require.NoError(t, err)
	assert.True(t, changed)
	b, _ = os.ReadFile(filepath.Join(dir, "mtls/leaf.crt"))
	assert.Equal(t, "CERT2", string(b))
}

func TestProjectFilesMissingSecret(t *testing.T) {
	_, _, err := projectFiles(
		map[string]string{"MTLS_LEAF_CERT_PATH": "mtls/leaf.crt"},
		map[string]string{}, t.TempDir(), os.Getuid(), os.Getgid())
	require.ErrorContains(t, err, "not found")
}

// TestProjectFilesDirMode: the runtime dir holding the leaf key must be 0700 so
// only its owner (and root) can list it — matching RuntimeDirectoryMode in the
// unit.
func TestProjectFilesDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	_, _, err := projectFiles(
		map[string]string{"MTLS_LEAF_CERT_PATH": "mtls/leaf.crt"},
		map[string]string{"mtls/leaf.crt": "CERT"}, dir, os.Getuid(), os.Getgid())
	require.NoError(t, err)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// The service user must OWN every directory from the RuntimeDir down to its PEM,
// not just the PEM. Those dirs are 0700, and the boot path's RuntimeDir is created
// by systemd as root:root (the unit has no User= — the agent drops privilege
// itself). If only the file is chowned, the service loses the SEARCH (+x) bit on
// its own directory and open() fails with EACCES after the privilege drop — it
// crash-loops on a key that is sitting right there. Nothing above the RuntimeDir is
// touched: /run/wardnet is shared and stays root-owned.
func TestOwnedDirChainCoversEveryLevelUpToBase(t *testing.T) {
	base := "/run/wardnet/tenants"

	// A nested grant key ("pki/daemon-jwt/key.pem") creates TWO levels below base;
	// both, plus base itself, must be owned by the service user.
	assert.Equal(t,
		[]string{
			"/run/wardnet/tenants/pki/daemon-jwt",
			"/run/wardnet/tenants/pki",
			"/run/wardnet/tenants",
		},
		ownedDirChain(base, "/run/wardnet/tenants/pki/daemon-jwt"))

	// A flat key still chowns the RuntimeDir itself — the root:root 0700 case.
	assert.Equal(t, []string{"/run/wardnet/tenants"}, ownedDirChain(base, base))

	// The chain never escapes base: /run/wardnet must stay root-owned.
	for _, p := range ownedDirChain(base, "/run/wardnet/tenants/mtls") {
		assert.True(t, strings.HasPrefix(p, base), "chain must not walk above the RuntimeDir, got %s", p)
	}
}

// The end-to-end shape: every directory created under the RuntimeDir is 0700 and
// owned by the projecting uid, so a nested key is reachable after the privilege
// drop. (Chowning to the current uid is a no-op the test user is allowed to make;
// the ownership rule itself is pinned by TestOwnedDirChainCoversEveryLevelUpToBase.)
func TestProjectFilesOwnsNestedDirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	_, _, err := projectFiles(
		map[string]string{"TENANTS_JWT_SIGNING_KEY_PATH": "pki/daemon-jwt/key.pem"},
		map[string]string{"pki/daemon-jwt/key.pem": "KEY-PEM"}, dir, os.Getuid(), os.Getgid())
	require.NoError(t, err)

	for _, p := range []string{dir, filepath.Join(dir, "pki"), filepath.Join(dir, "pki/daemon-jwt")} {
		info, err := os.Stat(p)
		require.NoError(t, err, "directory %s must exist", p)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "%s must be 0700", p)
	}
	info, err := os.Stat(filepath.Join(dir, "pki/daemon-jwt/key.pem"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o400), info.Mode().Perm(), "the PEM stays read-only to its owner")
}

// TestProjectFilesAtomicSet: when one file in a multi-file set fails, none of
// the set is committed — the service must never start with a leaf cert but a
// stale (or absent) matching key.
func TestProjectFilesAtomicSet(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"MTLS_LEAF_CERT_PATH": "mtls/leaf.crt",
		"MTLS_LEAF_KEY_PATH":  "mtls/leaf.key",
	}
	// leaf.key is missing from the provider — the whole set must roll back.
	_, _, err := projectFiles(files, map[string]string{"mtls/leaf.crt": "CERT"},
		dir, os.Getuid(), os.Getgid())
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(dir, "mtls/leaf.crt"))
	assert.True(t, os.IsNotExist(statErr), "no file is committed when any file in the set fails")
}

func TestProjectFilesEmpty(t *testing.T) {
	pathEnv, changed, err := projectFiles(nil, nil, t.TempDir(), os.Getuid(), os.Getgid())
	require.NoError(t, err)
	assert.Empty(t, pathEnv)
	assert.False(t, changed)
}
