package caddy

// installScript installs Caddy and prepares its conf.d directory. It is
// idempotent: re-running it on an already-provisioned host is a no-op beyond
// apt's own bookkeeping.
//
// It installs no secret tooling. Secrets are fetched at runtime by the Go
// inforge-bootstrap, which decrypts the host-key credential in-process — the
// host needs no jq/yq/sops/age. Only the packages the Caddy apt repository
// itself requires (apt-transport-https, curl, gnupg, the debian keyrings) are
// installed.
//
// The apt ordering bug that motivated this work is fixed here: the FIRST
// apt-get call is `apt-get update`, never `apt-get install`. The infra repo's
// cloud-init template installed debian-keyring before any update, so under
// `set -euo pipefail` the whole user-data aborted on first boot. Keeping the
// install out of cloud-init and ordering update-before-install avoids that.
//
// Commands run with sudo because inforge connects as the host's (sudo-capable)
// deploy user, not root.
const baseInstallScript = `#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# update BEFORE any install (the apt-order fix).
sudo -E apt-get update
sudo -E apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gnupg

# Caddy official apt repository (Cloudsmith).
if [ ! -f /usr/share/keyrings/caddy-stable-archive-keyring.gpg ]; then
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
fi
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
  | sudo tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
sudo -E apt-get update
sudo -E apt-get install -y caddy

# Caddy config layout: a base Caddyfile that imports per-service vhosts.
sudo install -d -m 0755 ` + ConfDir + `
sudo systemctl enable caddy
`

// installScript is the terminate-only realization (path A): the base install
// plus teardown of any layer4 override left by a previous path-B realization, so
// a host that loses its last passthrough/catch-all route reverts cleanly to the
// Caddyfile. Without this the stale systemd drop-in would keep the unit running
// the old caddy.json and ignore the freshly written Caddyfile. The teardown is
// idempotent: rm -f and reload are no-ops on a host that never ran path B.
const installScript = baseInstallScript + `
# Revert any layer4 override (path B -> path A): drop the systemd override and
# the native-JSON config so the unit falls back to the Caddyfile.
if [ -f /etc/systemd/system/caddy.service.d/inforge-l4.conf ]; then
  sudo rm -f /etc/systemd/system/caddy.service.d/inforge-l4.conf
  sudo systemctl daemon-reload
fi
sudo rm -f ` + L4ConfigPath + `
`

// InstallScript returns the host install script for the terminate-only Caddy
// realization. It installs Caddy (plus the packages its apt repository needs),
// prepares the conf.d directory, and reverts any prior layer4 override; the
// caller writes the base Caddyfile and per-service vhosts separately and reloads.
func InstallScript() string {
	return installScript
}

// installScriptL4 augments the base install for the layer4 realization (path B,
// hosts with passthrough/catch-all routes). The stock apt `caddy` package has no
// layer4 module, so after the base install it:
//   - downloads a layer4-capable Caddy from caddyserver.com's build service
//     (idempotent: skipped when the installed binary already has the module), and
//   - drops a systemd override pointing the unit at the native-JSON config
//     (/etc/caddy/caddy.json) instead of the Caddyfile, for both start and reload.
//
// The download is a fresh build (no published checksum to verify); it is fetched
// over HTTPS only. We only *send* the PROXY protocol to passthrough upstreams via
// the layer4 proxy handler, so only github.com/mholt/caddy-l4 is required.
const installScriptL4 = baseInstallScript + `
# --- layer4 realization (passthrough/catch-all host) ---
arch=$(uname -m)
case "$arch" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) echo "unsupported host arch: $arch" >&2; exit 1 ;;
esac

# Replace the stock binary with a layer4 build, unless it already has the module.
if ! /usr/bin/caddy list-modules 2>/dev/null | grep -q '^layer4$'; then
  tmp=$(mktemp)
  trap 'rm -f "$tmp"' EXIT
  curl -fsSL "https://caddyserver.com/api/download?os=linux&arch=${arch}&p=github.com/mholt/caddy-l4" -o "$tmp"
  sudo install -m 0755 "$tmp" /usr/bin/caddy
fi

# Point the unit at the native-JSON config (path B); clear the inherited values
# first so the override fully replaces ExecStart/ExecReload.
sudo install -d -m 0755 /etc/systemd/system/caddy.service.d
sudo tee /etc/systemd/system/caddy.service.d/inforge-l4.conf >/dev/null <<'UNIT'
[Service]
ExecStart=
ExecStart=/usr/bin/caddy run --environ --config ` + L4ConfigPath + `
ExecReload=
ExecReload=/usr/bin/caddy reload --config ` + L4ConfigPath + ` --force
UNIT
sudo systemctl daemon-reload
sudo systemctl enable caddy
`

// InstallScriptL4 returns the host install script for the layer4 realization:
// the base Caddy install plus a layer4-capable binary and a systemd override
// pointing the unit at the native-JSON config. The caller writes
// caddy.RenderL4Config output to L4ConfigPath and then reloads Caddy.
func InstallScriptL4() string {
	return installScriptL4
}
