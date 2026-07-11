package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// projectFiles writes each of the descriptor's files: entries (env-var → provider
// key) as a PEM file under dir, mode 0400 owned by uid:gid. The agent runs
// as root and chowns to the service user before dropping privilege. The provider
// key (e.g. "mtls/leaf.crt") is preserved as the relative on-disk path so distinct
// keys never collide. The whole set lands atomically: every file is staged to a
// temp first, then renamed in a second pass — so a failure before the renames
// leaves the existing set untouched (never a mismatched cert/key pair). It returns
// the `<env-var>=<path>` entries to add to the service env and whether any file's
// content differs from what is already on disk (the renewal projector uses
// `changed` to decide whether to reload).
//
// dir and every directory created beneath it are chowned to uid:gid too, not just
// the files. A 0700 directory owned by root denies the service user the SEARCH
// (+x) permission it needs to traverse to its own PEM, so chowning the file alone
// yields EACCES on open after the privilege drop — the service crash-loops on a
// key that is sitting right there. This matters because the boot path's dir comes
// from the unit's `RuntimeDirectory=` + `RuntimeDirectoryMode=0700` and the unit
// has NO `User=` (the agent drops privilege itself), so systemd hands us a
// root:root 0700 dir. The mesh proxy avoids the trap by declaring no
// RuntimeDirectory= and seeding its dirs with `install -d -o nginx` instead
// (meshnginx.SeedScript); a service has no such seed, so ownership is fixed here —
// the one place every projection path goes through.
func projectFiles(files, secrets map[string]string, dir string, uid, gid int) (pathEnv []string, changed bool, err error) {
	if len(files) == 0 {
		return nil, false, nil
	}
	// 0o700: the directory holds a leaf private key. systemd's
	// RuntimeDirectoryMode governs the dir on the boot path; this matches it for
	// the renewal/nested-create path so the mode is never widened.
	if err := mkdirOwned(dir, dir, uid, gid); err != nil {
		return nil, false, err
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
		tmp, err := stageFile(dir, dest, []byte(val), uid, gid)
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
	existing, err := os.ReadFile(dest) // #nosec G304 -- dir is the deploy-tool-controlled RuntimeDir arg; key is a fixed meshpaths provider key
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("read %s: %w", dest, err)
	}
	return !bytes.Equal(existing, want), nil
}

// mkdirOwned creates dir (with any missing parents) and chowns dir and every
// level between it and base — inclusive of both — to uid:gid, mode 0700. It is
// the only way a directory is created on a projection path, so a projected PEM is
// always reachable by the service user: 0700 grants the OWNER search permission,
// and the owner is now the service, not root.
//
// It never touches a directory above base (base is the service's RuntimeDir; its
// parents, e.g. /run/wardnet, are shared and stay root-owned).
func mkdirOwned(base, dir string, uid, gid int) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	for _, p := range ownedDirChain(base, dir) {
		if err := os.Chown(p, uid, gid); err != nil {
			return fmt.Errorf("chown dir %s: %w", p, err)
		}
	}
	return nil
}

// ownedDirChain returns every directory that must be owned by the service user
// for it to reach a file under dir: dir itself, each level between dir and base,
// and base. Nothing above base is included — base is the service's RuntimeDir, and
// its parents (e.g. /run/wardnet) are shared and stay root-owned.
//
// A key like "pki/daemon-jwt/key.pem" creates TWO levels below base and both need
// the owner's search bit, which is the whole point of walking the chain rather
// than chowning only the leaf directory.
func ownedDirChain(base, dir string) []string {
	base = filepath.Clean(base)
	var chain []string
	for p := filepath.Clean(dir); strings.HasPrefix(p, base); p = filepath.Dir(p) {
		chain = append(chain, p)
		if p == base {
			break
		}
	}
	return chain
}

// stageFile writes content to a temp file beside dest (mode 0400, owned uid:gid)
// and returns the temp path for the caller to rename into place. On any error it
// removes the temp so no key material lingers. base is the projection root, so any
// directory stageFile has to create beneath it is owned by the service user too.
func stageFile(base, dest string, content []byte, uid, gid int) (string, error) {
	if err := mkdirOwned(base, filepath.Dir(dest), uid, gid); err != nil {
		return "", err
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
