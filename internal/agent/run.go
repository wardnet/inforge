package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wardnet/inforge/internal/hostpaths"
	"github.com/wardnet/inforge/internal/hostsecrets"
	"github.com/wardnet/inforge/internal/meshpaths"
)

// Descriptor / secrets / leaf filenames within a service's (or the mesh
// proxy's) on-host directory (ADR-0035). secretsFile is deploy-owned (env vars
// + grant secrets); leafFile is renew-owned (mesh/mtls leaf material) and is
// written by a later slice (`inforge pki renew`'s SSH push) — both are
// optional, and a fully secret-less service has neither.
const (
	descriptorFile = "descriptor.yaml"
	secretsFile    = "secrets.age"
	leafFile       = "leaf.age"
)

// Run is the inforge-agent entry point. It has two modes: ExecStart=
// /usr/local/bin/inforge-agent <service-dir> — the boot path — and
// ExecStartPre=-inforge-agent mesh-project <mesh-dir> — the mesh proxy's
// pre-start local projection (nginx cannot decrypt age files itself). It
// loads the descriptor, resolves the run-as user, decrypts whichever of
// secrets.age / leaf.age exist in dir and merges them, projects any files:
// entries into tmpfs, builds the env, then drops privilege and execs the
// real service binary.
//
// ADR-0035 retired the renewal PULL model (`inforge-agent project` and the
// old network-fetching `mesh-project`): with secrets.age/leaf.age persistent
// on disk, there is nothing left to poll or retry against over the network —
// renewal is now a push (`inforge pki renew` SSHes the new leaf.age directly
// to the host and signals reload/restart). `mesh-project` survives as a much
// simpler LOCAL operation: a one-shot, network-free decrypt-and-project of
// whatever leaf.age already sits on disk, run every boot (including a reboot,
// which clears the mesh proxy's tmpfs RuntimeDir) so nginx never starts
// without its cert material re-projected.
//
// Any error returns to main, which exits non-zero so systemd restarts the unit.
func Run(args []string, version string) error {
	if len(args) == 2 && (args[1] == "--version" || args[1] == "version") {
		fmt.Println(version)
		return nil
	}
	if len(args) == 3 && args[1] == "mesh-project" {
		return runMeshProject(args[2])
	}
	if len(args) == 3 && args[1] == "project-leaf" {
		return runProjectLeaf(args[2])
	}
	if len(args) == 2 {
		return runBoot(args[1])
	}
	return fmt.Errorf("usage: inforge-agent <service-dir> | inforge-agent mesh-project <mesh-dir> | inforge-agent project-leaf <service-dir>")
}

// runMeshProject is the mesh proxy's pre-start local projection: it decrypts
// AgentDir/leaf.age (if present — absent on a fresh host before the first
// `inforge pki renew`/deploy baseline has pushed one, in which case this is a
// no-op and the placeholder seed script fills the gap) and writes every file
// it carries under meshpaths.RuntimeDir, owned by the nginx user, mirroring
// each key's own relative path (meshpaths.LeafCertKey/LeafKeyKey/BundleKey are
// constructed so RuntimeDir+"/"+key == the on-host path by construction). It
// never touches the network — purely a local decrypt-and-write.
func runMeshProject(dir string) error {
	path := filepath.Join(dir, leafFile)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	blob, err := DecryptSecretsBlob(path, defaultHostKeyPath)
	if err != nil {
		return fmt.Errorf("decrypt %s: %w", path, err)
	}
	nginxUser, err := lookupUser("nginx")
	if err != nil {
		return err
	}
	// files is the identity map projectFiles expects (env-var → provider key):
	// the mesh proxy has no env vars, so each file's own key doubles as its
	// "env var" name, landing it at RuntimeDir/<key>.
	files := make(map[string]string, len(blob.Files))
	for k := range blob.Files {
		files[k] = k
	}
	_, _, err = projectFiles(files, blob.Files, meshpaths.RuntimeDir, nginxUser.uid, nginxUser.gid)
	return err
}

// runProjectLeaf is the mtls_files: true service analogue of runMeshProject: a
// local, network-free decrypt-and-project of whatever leaf.age already sits in
// dir (a service's DescriptorDir), run over SSH by `inforge pki renew`'s push
// (cmd/inforge/pki.go's renewSet) immediately BEFORE it signals
// reload-or-restart. It exists because a service that declares reload: gets an
// ExecReload= in its unit, so systemd's reload-or-restart always resolves to a
// plain reload — which re-reads whatever leaf.age's PEMs are already on disk
// but never re-triggers the decrypt (that only happens once, in runBoot, at
// ExecStart). Without this step a reload after `inforge pki renew` would leave
// the process serving its OLD cert until an unrelated full restart. Absent
// leaf.age (not yet pushed) is a no-op, matching runMeshProject.
func runProjectLeaf(dir string) error {
	path := filepath.Join(dir, leafFile)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	desc, err := LoadDescriptor(filepath.Join(dir, descriptorFile))
	if err != nil {
		return err
	}
	user, err := lookupUser(desc.User)
	if err != nil {
		return err
	}
	blob, err := DecryptSecretsBlob(path, defaultHostKeyPath)
	if err != nil {
		return fmt.Errorf("decrypt %s: %w", path, err)
	}
	_, _, err = projectFiles(desc.Files, blob.Files, hostpaths.RuntimeDir(desc.Service), user.uid, user.gid)
	return err
}

// runBoot is the ExecStart path: decrypt secrets.age/leaf.age, project any
// files: entries, build the env, then drop privilege and exec the service.
func runBoot(dir string) error {
	desc, err := LoadDescriptor(filepath.Join(dir, descriptorFile))
	if err != nil {
		return err
	}
	user, err := lookupUser(desc.User)
	if err != nil {
		return err
	}
	blob, err := loadSecretsBlobs(dir)
	if err != nil {
		return err
	}

	// Project files: entries (mesh/mtls PEMs) into the tmpfs RuntimeDirectory,
	// mode 0400 owned by the service user — done while still root, before the
	// privilege drop. The *_PATH vars are not in desc.Env, so they are appended
	// after buildEnv.
	pathEnv, _, err := projectFiles(desc.Files, blob.Files, hostpaths.RuntimeDir(desc.Service), user.uid, user.gid)
	if err != nil {
		return err
	}

	envv, err := buildEnv(desc, blob.Env, user.home, newInstanceID())
	if err != nil {
		return err
	}
	envv = append(envv, pathEnv...)

	return dropAndExec(desc.Exec, user.uid, user.gid, envv)
}

// loadSecretsBlobs decrypts whichever of secrets.age (deploy-owned) and
// leaf.age (renew-owned) are present in dir and merges their Env/Files maps in
// memory — never on disk. Both are optional: a fully secret-less service (no
// env/grants, not mtls_files:, not the mesh proxy) has neither and this
// returns a zero Blob. The two artifacts carry disjoint keysets by
// construction (deploy resolves env/grant values; renew resolves leaf/bundle
// material), so a key present in both is a producer bug — rejected rather than
// silently resolved one way.
func loadSecretsBlobs(dir string) (hostsecrets.Blob, error) {
	var merged hostsecrets.Blob
	for _, name := range []string{secretsFile, leafFile} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return hostsecrets.Blob{}, fmt.Errorf("stat %s: %w", path, err)
		}
		b, err := DecryptSecretsBlob(path, defaultHostKeyPath)
		if err != nil {
			return hostsecrets.Blob{}, fmt.Errorf("decrypt %s: %w", path, err)
		}
		if err := mergeSecretsBlob(&merged, b); err != nil {
			return hostsecrets.Blob{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	return merged, nil
}

// mergeSecretsBlob merges src's Env/Files into dst, failing on any key already
// present in dst — secrets.age and leaf.age must never claim the same key.
func mergeSecretsBlob(dst *hostsecrets.Blob, src hostsecrets.Blob) error {
	for k, v := range src.Env {
		if dst.Env == nil {
			dst.Env = map[string]string{}
		}
		if _, dup := dst.Env[k]; dup {
			return fmt.Errorf("env key %q is present in more than one secrets blob", k)
		}
		dst.Env[k] = v
	}
	for k, v := range src.Files {
		if dst.Files == nil {
			dst.Files = map[string]string{}
		}
		if _, dup := dst.Files[k]; dup {
			return fmt.Errorf("file key %q is present in more than one secrets blob", k)
		}
		dst.Files[k] = v
	}
	return nil
}

// newInstanceID returns a fresh, per-(re)start unique identifier for the service
// instance — 16 random bytes hex-encoded — injected as INFORGE_INSTANCE_ID (the
// OTel service.instance.id). Because runBoot runs on every (re)start, a new value
// is minted each time, distinguishing replicas and restarts. crypto/rand.Read does
// not fail in practice; on the impossible error we return "" so the boot still
// proceeds (a missing telemetry label is acceptable; a failed start is not).
func newInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
