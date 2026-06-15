package bootstrapper

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// runtimeDir is the tmpfs directory a service's projected PEMs live in. It
// matches the service unit's `RuntimeDirectory=wardnet/<service>` — systemd
// creates and cleans /run/wardnet/<service> (RAM-backed). The renewal projector
// writes to the same known path WITHOUT declaring its own RuntimeDirectory
// (which systemd would remove when the oneshot stops, deleting the running
// service's material). Keep in sync with internal/service.RuntimeDir.
func runtimeDir(service string) string {
	return "/run/wardnet/" + service
}

// unitName mirrors internal/service.UnitName — the unit the renewal projector
// reloads. Duplicated (not imported) to keep the static inforge-bootstrap binary
// decoupled from the deploy-side service package.
func unitName(service string) string {
	return "wardnet-" + service + ".service"
}

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
// key) as a PEM file under dir, atomically (temp → rename), mode 0400 owned by
// uid:gid. The bootstrapper runs as root, so it chowns to the service user before
// dropping privilege. The provider key (e.g. "mtls/leaf.crt") is preserved as the
// relative on-disk path, so distinct keys never collide. It returns the
// `<env-var>=<path>` entries to add to the service env and whether any file's
// content changed from what was already on disk (the renewal projector uses
// `changed` to decide whether to reload).
func projectFiles(files, secrets map[string]string, dir string, uid, gid int) (pathEnv []string, changed bool, err error) {
	if len(files) == 0 {
		return nil, false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, fmt.Errorf("create runtime dir %s: %w", dir, err)
	}
	envVars := make([]string, 0, len(files))
	for k := range files {
		envVars = append(envVars, k)
	}
	sort.Strings(envVars) // deterministic env output

	for _, envVar := range envVars {
		key := files[envVar]
		val, ok := secrets[key]
		if !ok || val == "" {
			return nil, false, fmt.Errorf("mesh material %q for %s not found or empty in the provider", key, envVar)
		}
		dest := filepath.Join(dir, filepath.FromSlash(key))
		wrote, err := writeIfChanged(dest, []byte(val), uid, gid)
		if err != nil {
			return nil, false, err
		}
		changed = changed || wrote
		pathEnv = append(pathEnv, envVar+"="+dest)
	}
	return pathEnv, changed, nil
}

// writeIfChanged atomically writes content to dest (mode 0400, owned uid:gid)
// unless the file already holds identical content. Returns whether it wrote.
func writeIfChanged(dest string, content []byte, uid, gid int) (bool, error) {
	if existing, err := os.ReadFile(dest); err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, fmt.Errorf("create dir for %s: %w", dest, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temp for %s: %w", dest, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // best-effort; a no-op after a successful rename
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write %s: %w", dest, err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close %s: %w", dest, err)
	}
	if err := os.Chmod(tmpName, 0o400); err != nil {
		return false, fmt.Errorf("chmod %s: %w", dest, err)
	}
	if err := os.Chown(tmpName, uid, gid); err != nil {
		return false, fmt.Errorf("chown %s: %w", dest, err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return false, fmt.Errorf("rename to %s: %w", dest, err)
	}
	return true, nil
}
