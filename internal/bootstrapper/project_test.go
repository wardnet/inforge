package bootstrapper

import (
	"os"
	"path/filepath"
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

func TestReloadUnitUsesReloadOrRestart(t *testing.T) {
	var got []string
	orig := systemctl
	systemctl = func(args ...string) error { got = args; return nil }
	defer func() { systemctl = orig }()

	require.NoError(t, reloadUnit("bridge"))
	assert.Equal(t, []string{"reload-or-restart", "wardnet-bridge.service"}, got)
}
