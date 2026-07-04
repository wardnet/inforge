package meshnginx

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wardnet/inforge/internal/hostpaths"
	"github.com/wardnet/inforge/internal/meshpaths"
)

// The mesh proxy runs as a SECOND nginx instance (unit meshpaths.UnitName),
// separate from the north-south nginx (ADR-0032). It shares the nginx binary the
// north-south install already puts on the host (installed via internal/nginx),
// but runs its own config (meshpaths.ConfigPath), pid file (meshpaths.PIDPath),
// and systemd unit, so the two never share a process. These paths are the
// deploy-side on-host names the mesh provider writes; the runtime scheme
// (ports, RuntimeDir, leaf/bundle paths) lives in meshpaths, the shared source
// of truth.
const (
	// UnitPath is the systemd unit file the mesh nginx runs as.
	UnitPath = "/etc/systemd/system/" + meshpaths.UnitName + ".service"
	// RenewUnitName / RenewUnitPath / RenewTimerPath are the mesh material renewal
	// oneshot + timer (the pull half of ADR-0033's leaf delivery).
	RenewUnitName  = meshpaths.UnitName + "-renew"
	RenewUnitPath  = "/etc/systemd/system/" + RenewUnitName + ".service"
	RenewTimerPath = "/etc/systemd/system/" + RenewUnitName + ".timer"
	// SeedScriptPath is the on-host placeholder-seed script the unit runs as an
	// ExecStartPre, so a reboot (which clears the tmpfs RuntimeDir) self-heals to
	// a valid — if placeholder — cert set rather than crash-looping.
	SeedScriptPath = "/etc/wardnet/mesh-seed.sh"
	// nginxBin is the nginx binary the north-south install provides.
	nginxBin = "/usr/sbin/nginx"
)

// UnitFile returns the systemd unit for the mesh nginx. It is Type=forking (nginx
// daemonizes and writes meshpaths.PIDPath, matching the config's `pid` directive).
// Before starting, the unit (1) pulls REAL mesh material from the provider
// (`inforge-agent mesh-project`, `-`-prefixed so a degraded pull never blocks
// the proxy — ADR-0033; this is also how a reboot's cleared tmpfs re-seeds
// real leaves. The binary itself fails HARD, so the un-prefixed renew oneshot
// surfaces persistent pull breakage as a failed unit),
// (2) re-seeds placeholder material for anything still
// absent (SeedScriptPath — first boot, before the deploy baseline lands), and
// (3) validates the config — so a start never races an empty RuntimeDir or a bad
// config. It declares NO systemd RuntimeDirectory= — that would let systemd wipe
// the tmpfs cert dir when the unit stops; the pull + seed own the dir's
// lifecycle instead.
func UnitFile() string {
	return `[Unit]
Description=wardnet east-west mesh proxy (nginx)
After=network-online.target
Wants=network-online.target

[Service]
Type=forking
PIDFile=` + meshpaths.PIDPath + `
ExecStartPre=-` + hostpaths.AgentBin + ` mesh-project ` + meshpaths.AgentDir + `
ExecStartPre=/usr/bin/env bash ` + SeedScriptPath + `
ExecStartPre=` + nginxBin + ` -t -c ` + meshpaths.ConfigPath + `
ExecStart=` + nginxBin + ` -c ` + meshpaths.ConfigPath + `
ExecReload=` + nginxBin + ` -s reload -c ` + meshpaths.ConfigPath + `
ExecStop=` + nginxBin + ` -s quit -c ` + meshpaths.ConfigPath + `
Restart=on-failure

[Install]
WantedBy=multi-user.target
`
}

// RenewUnitFile is the oneshot that re-pulls the mesh material and reloads the
// proxy on a real change — `inforge pki renew` only writes the provider; mesh
// hosts converge on their own through this unit (ADR-0033). It must NOT declare
// RuntimeDirectory= (systemd would delete the running proxy's tmpfs cert dir
// when the oneshot stops — the same trap as the per-service renew oneshot).
func RenewUnitFile() string {
	return `[Unit]
Description=wardnet mesh proxy cert material renewal
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
ExecStart=` + hostpaths.AgentBin + ` mesh-project ` + meshpaths.AgentDir + `
`
}

// RenewTimerFile schedules the daily mesh material pull. Daily with a jitter
// hour is far inside the 90-day leaf TTL; Persistent=true catches up after
// downtime.
func RenewTimerFile() string {
	return `[Unit]
Description=daily wardnet mesh proxy cert renewal

[Timer]
OnCalendar=daily
RandomizedDelaySec=1h
Persistent=true

[Install]
WantedBy=timers.target
`
}

// SeedScript renders the idempotent placeholder-seed shell for a mesh host. It
// creates the tmpfs RuntimeDir and, for the trust bundle and every co-located
// service's leaf, generates a short-lived self-signed placeholder ONLY when the
// file is absent — so it never clobbers real material a later leaf-delivery pass
// (or a renewal) writes. Its sole purpose is to keep `nginx -t` green and the
// unit running before real leaves land: a mesh proxy with placeholder material
// starts cleanly but does not yet carry real peer traffic (the CN/SNI and trust
// anchor are placeholders). This mirrors the placeholder-bundle idiom apps use
// (internal/app.PlaceholderIndexHTML) so realization never fails on absent state.
//
// services must be the full set of co-located mesh (pki:) service names — every
// one is an egress caller and so needs a leaf present for the egress client cert.
func SeedScript(services []string) string {
	names := append([]string(nil), services...)
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -euo pipefail\n")
	b.WriteString("umask 077\n")
	// The RuntimeDir is tmpfs, owned by the nginx worker user (the config's
	// `user nginx`) so the workers can read the leaf keys.
	fmt.Fprintf(&b, "install -d -m 0755 %s\n", parentDir(meshpaths.RuntimeDir))
	fmt.Fprintf(&b, "install -d -m 0700 -o nginx -g nginx %s\n", meshpaths.RuntimeDir)
	// A single self-signed placeholder CA stands in for the trust bundle; nginx
	// loads it as the client-cert verification anchor (ssl_client_certificate).
	b.WriteString(genPlaceholder(meshpaths.BundlePath, "", "inforge-mesh-placeholder-ca"))
	for _, svc := range names {
		dir := meshpaths.RuntimeDir + "/" + svc
		fmt.Fprintf(&b, "install -d -m 0700 -o nginx -g nginx %s\n", dir)
		b.WriteString(genPlaceholder(meshpaths.LeafCertPath(svc), meshpaths.LeafKeyPath(svc), "inforge-mesh-placeholder"))
	}
	return b.String()
}

// genPlaceholder emits the shell to create a self-signed cert (and, when keyPath
// is set, its key) at the given paths only when the cert is absent. A key-less
// call (keyPath == "") writes a stand-alone cert file (the trust bundle), keeping
// the freshly-generated key in a temp file that is removed. Files are owned by the
// nginx user, mode 0400, so only nginx (and root) can read them.
func genPlaceholder(certPath, keyPath, cn string) string {
	if keyPath == "" {
		keyPath = certPath + ".seedkey"
		return fmt.Sprintf(`if [ ! -f %[1]s ]; then
  openssl req -x509 -newkey ed25519 -nodes -days 1 -subj %[3]s -keyout %[2]s -out %[1]s >/dev/null 2>&1
  chown nginx:nginx %[1]s; chmod 0444 %[1]s; rm -f %[2]s
fi
`, certPath, keyPath, "/CN="+cn)
	}
	return fmt.Sprintf(`if [ ! -f %[1]s ] || [ ! -f %[2]s ]; then
  openssl req -x509 -newkey ed25519 -nodes -days 1 -subj %[3]s -keyout %[2]s -out %[1]s >/dev/null 2>&1
  chown nginx:nginx %[1]s %[2]s; chmod 0444 %[1]s; chmod 0400 %[2]s
fi
`, certPath, keyPath, "/CN="+cn)
}

// parentDir returns the parent directory of a path (its longest prefix before the
// final "/"). meshpaths.RuntimeDir is "/run/wardnet/mesh", so this yields
// "/run/wardnet" — created before the RuntimeDir itself.
func parentDir(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return p
}
