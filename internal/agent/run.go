package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/wardnet/inforge/internal/hostpaths"
	"github.com/wardnet/inforge/internal/meshpaths"
)

// Descriptor and credential filenames within a service's on-host directory.
const (
	descriptorFile = "descriptor.yaml"
	credentialFile = "credential.age"
)

// Run is the inforge-agent entry point. Invoked by systemd two ways:
//   - ExecStart=/usr/local/bin/inforge-agent <service-dir>: the boot path —
//     load the descriptor, resolve the run-as user, fetch the service's secrets,
//     project any mesh PEMs into tmpfs, build the env, then drop privilege and
//     exec the real service binary.
//   - inforge-agent project <service-dir>: the renewal path (a per-service
//     timer) — re-project the current leaf and reload the unit if it changed.
//   - inforge-agent mesh-project <mesh-dir>: the mesh proxy's material pull
//     (ADR-0033) — fetch every co-located service's leaf + the trust bundle and
//     project them into the mesh tmpfs; runs as the mesh unit's ExecStartPre and
//     from the wardnet-mesh-renew timer.
//
// Any error returns to main, which exits non-zero so systemd restarts the unit.
func Run(args []string, version string) error {
	if len(args) == 2 && (args[1] == "--version" || args[1] == "version") {
		fmt.Println(version)
		return nil
	}
	switch {
	case len(args) == 3 && args[1] == "project":
		return runProject(args[2])
	case len(args) == 3 && args[1] == "mesh-project":
		return runMeshProject(args[2])
	case len(args) == 2:
		return runBoot(args[1])
	default:
		return fmt.Errorf("usage: inforge-agent <service-dir> | inforge-agent project <service-dir> | inforge-agent mesh-project <mesh-dir>")
	}
}

// runBoot is the ExecStart path: fetch + project mesh PEMs + exec the service.
func runBoot(dir string) error {
	ctx := context.Background()

	desc, err := LoadDescriptor(filepath.Join(dir, descriptorFile))
	if err != nil {
		return err
	}
	user, err := lookupUser(desc.User)
	if err != nil {
		return err
	}
	secrets, err := fetchSecrets(ctx, dir, desc.Provider)
	if err != nil {
		return err
	}

	// Project mesh PEMs (descriptor files:) into the tmpfs RuntimeDirectory, mode
	// 0400 owned by the service user — done while still root, before the privilege
	// drop. The *_PATH vars are not in desc.Env, so they are appended after buildEnv.
	pathEnv, _, err := projectFiles(desc.Files, secrets, hostpaths.RuntimeDir(desc.Service), user.uid, user.gid)
	if err != nil {
		return err
	}

	envv, err := buildEnv(desc, secrets, user.home, newInstanceID())
	if err != nil {
		return err
	}
	envv = append(envv, pathEnv...)

	return dropAndExec(desc.Exec, user.uid, user.gid, envv)
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

// runProject is the renewal-timer path: re-fetch and re-project the mesh PEMs for
// a running service, and reload-or-restart the unit only if a file changed. A
// stopped service is skipped — it projects fresh on its next start.
func runProject(dir string) error {
	ctx := context.Background()

	desc, err := LoadDescriptor(filepath.Join(dir, descriptorFile))
	if err != nil {
		return err
	}
	if len(desc.Files) == 0 || !unitActive(desc.Service) {
		return nil
	}
	user, err := lookupUser(desc.User)
	if err != nil {
		return err
	}
	secrets, err := fetchSecrets(ctx, dir, desc.Provider)
	if err != nil {
		return err
	}
	_, changed, err := projectFiles(desc.Files, secrets, hostpaths.RuntimeDir(desc.Service), user.uid, user.gid)
	if err != nil {
		return err
	}
	if changed {
		return reloadUnit(desc.Service)
	}
	return nil
}

// meshOwner is the user the mesh proxy's material is owned by: the mesh nginx
// reads the leaves/bundle, nothing else may.
const meshOwner = "nginx"

// runMeshProject is the mesh proxy's material pull (ADR-0033): fetch each
// co-located service's leaf cert+key and the shared trust bundle from the
// host's own provider path and project them atomically into the mesh tmpfs
// (meshpaths.RuntimeDir), owned by nginx. It runs as the wardnet-mesh unit's
// ExecStartPre (so a reboot re-seeds REAL material before nginx starts) and
// from the daily wardnet-mesh-renew timer (renewal convergence).
//
// Pull failures return an ERROR (non-zero exit). A failure never touches the
// existing on-disk set (the projection is an atomic all-or-nothing), and the
// two invocation paths handle it differently by design: the unit's
// ExecStartPre line is `-`-prefixed, so a degraded pull never blocks the
// proxy from starting on whatever is on disk (the seed script backstops first
// boot with placeholders); the renew oneshot has no such prefix, so a
// persistently broken pull (stale credential, missing material) lands the
// timer unit in systemd's failed state — the monitorable signal that keeps a
// silent 90-day drift to leaf expiry from happening.
func runMeshProject(dir string) error {
	ctx := context.Background()

	desc, err := LoadMeshDescriptor(filepath.Join(dir, descriptorFile))
	if err != nil {
		return err
	}
	user, err := lookupUser(meshOwner)
	if err != nil {
		return err
	}
	secrets, err := fetchSecrets(ctx, dir, desc.Provider)
	if err != nil {
		return err
	}
	files := desc.Files()
	_, changed, err := projectFiles(files, secrets, meshpaths.RuntimeDir, user.uid, user.gid)
	if err != nil {
		return err
	}
	// Reload only on a real change AND a running proxy: at ExecStartPre the unit
	// is still starting (not active) and needs no reload; on the timer a changed
	// leaf reloads the mesh nginx (its ExecReload — no downtime).
	if changed && systemctl("is-active", "--quiet", meshpaths.UnitName) == nil {
		return systemctl("reload-or-restart", meshpaths.UnitName)
	}
	return nil
}

// fetchSecrets decrypts the provider credential in dir and fetches the secrets
// under the provider's path. A provider-less caller (a secret-less service) has
// nothing to fetch — there is no credential.age on disk — and gets a nil map.
func fetchSecrets(ctx context.Context, dir string, provider Provider) (map[string]string, error) {
	if provider.Kind == "" {
		return nil, nil
	}
	credential, err := DecryptCredential(filepath.Join(dir, credentialFile), defaultHostKeyPath)
	if err != nil {
		return nil, err
	}
	fetcher, err := newFetcher(provider, credential)
	if err != nil {
		return nil, err
	}
	return FetchWithBackoff(ctx, fetcher, realClock{}, baseDelay, maxDelay, budget)
}

// unitActive reports whether the service unit is currently running.
func unitActive(service string) bool {
	return systemctl("is-active", "--quiet", hostpaths.UnitName(service)) == nil
}

// reloadUnit applies a renewed leaf to a running service. reload-or-restart
// reloads when the unit defines ExecReload (the service declared `reload:`),
// otherwise restarts — so renewal is no-downtime when the service supports it.
func reloadUnit(service string) error {
	return systemctl("reload-or-restart", hostpaths.UnitName(service))
}

// newFetcher selects the SecretsFetcher implementation for the provider kind.
// New providers are added here as new implementations.
func newFetcher(p Provider, credential []byte) (SecretsFetcher, error) {
	switch p.Kind {
	case "infisical":
		return newInfisicalFetcher(p, credential)
	default:
		return nil, fmt.Errorf("unsupported provider kind %q", p.Kind)
	}
}
