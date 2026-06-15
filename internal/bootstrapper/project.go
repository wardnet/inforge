package bootstrapper

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// systemctl runs a systemctl command; overridden in tests. The renewal projector
// uses it to check the unit is active and to reload-or-restart it after a leaf
// changes.
var systemctl = func(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %v: %w: %s", args, err, bytes.TrimSpace(out))
	}
	return nil
}

// projectFiles writes each of the descriptor's files: entries (env-var → provider
// key) as a PEM file under dir, mode 0400 owned by uid:gid. The bootstrapper runs
// as root and chowns to the service user before dropping privilege. The provider
// key (e.g. "mtls/leaf.crt") is preserved as the relative on-disk path so distinct
// keys never collide. The whole set lands atomically: every file is staged to a
// temp first, then renamed in a second pass — so a failure before the renames
// leaves the existing set untouched (never a mismatched cert/key pair). It returns
// the `<env-var>=<path>` entries to add to the service env and whether any file's
// content differs from what is already on disk (the renewal projector uses
// `changed` to decide whether to reload).
func projectFiles(files, secrets map[string]string, dir string, uid, gid int) (pathEnv []string, changed bool, err error) {
	if len(files) == 0 {
		return nil, false, nil
	}
	// 0o700: the directory holds a leaf private key. systemd's
	// RuntimeDirectoryMode governs the dir on the boot path; this matches it for
	// the renewal/nested-create path so the mode is never widened.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, fmt.Errorf("create runtime dir %s: %w", dir, err)
	}
	envVars := make([]string, 0, len(files))
	for k := range files {
		envVars = append(envVars, k)
	}
	sort.Strings(envVars) // deterministic env output

	type pendingRename struct{ tmp, dest string }
	var pending []pendingRename
	committed := false
	defer func() {
		if !committed {
			for _, p := range pending {
				_ = os.Remove(p.tmp)
			}
		}
	}()

	// First pass: stage every changed file to a temp (no dest mutated yet).
	for _, envVar := range envVars {
		key := files[envVar]
		val, ok := secrets[key]
		if !ok || val == "" {
			return nil, false, fmt.Errorf("mesh material %q for %s not found or empty in the provider", key, envVar)
		}
		dest := filepath.Join(dir, filepath.FromSlash(key))
		pathEnv = append(pathEnv, envVar+"="+dest)

		diff, err := differs(dest, []byte(val))
		if err != nil {
			return nil, false, err
		}
		if !diff {
			continue
		}
		tmp, err := stageFile(dest, []byte(val), uid, gid)
		if err != nil {
			return nil, false, err
		}
		pending = append(pending, pendingRename{tmp: tmp, dest: dest})
		changed = true
	}

	// Second pass: commit. Renames within the same tmpfs dir don't fail once the
	// temps exist, so the set swaps in together.
	for _, p := range pending {
		if err := os.Rename(p.tmp, p.dest); err != nil {
			return nil, false, fmt.Errorf("rename to %s: %w", p.dest, err)
		}
	}
	committed = true
	return pathEnv, changed, nil
}

// differs reports whether dest is absent or holds content other than want.
func differs(dest string, want []byte) (bool, error) {
	existing, err := os.ReadFile(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("read %s: %w", dest, err)
	}
	return !bytes.Equal(existing, want), nil
}

// stageFile writes content to a temp file beside dest (mode 0400, owned uid:gid)
// and returns the temp path for the caller to rename into place. On any error it
// removes the temp so no key material lingers.
func stageFile(dest string, content []byte, uid, gid int) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", fmt.Errorf("create dir for %s: %w", dest, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temp for %s: %w", dest, err)
	}
	tmpName := tmp.Name()
	clean := func(e error) (string, error) { _ = os.Remove(tmpName); return "", e }
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return clean(fmt.Errorf("write %s: %w", dest, err))
	}
	if err := tmp.Close(); err != nil {
		return clean(fmt.Errorf("close %s: %w", dest, err))
	}
	if err := os.Chmod(tmpName, 0o400); err != nil {
		return clean(fmt.Errorf("chmod %s: %w", dest, err))
	}
	if err := os.Chown(tmpName, uid, gid); err != nil {
		return clean(fmt.Errorf("chown %s: %w", dest, err))
	}
	return tmpName, nil
}
