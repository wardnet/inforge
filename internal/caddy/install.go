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
const installScript = `#!/usr/bin/env bash
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

// InstallScript returns the host install script for the Caddy realization. It
// installs Caddy (plus the packages its apt repository needs) and prepares the
// conf.d directory; the caller writes the base Caddyfile and per-service vhosts
// separately and then reloads Caddy.
func InstallScript() string {
	return installScript
}
