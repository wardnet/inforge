package caddy

// installScript installs Caddy and the host tooling the inforge service
// bootstrapper relies on (jq, yq, sops, age). It is idempotent: re-running it on
// an already-provisioned host is a no-op beyond apt's own bookkeeping.
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
sudo -E apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gnupg jq age

# Caddy official apt repository (Cloudsmith).
if [ ! -f /usr/share/keyrings/caddy-stable-archive-keyring.gpg ]; then
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
fi
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
  | sudo tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
sudo -E apt-get update
sudo -E apt-get install -y caddy

# yq and sops are not in Debian stable apt; install pinned static binaries and
# verify each against a SHA-256 pinned from the publisher's release checksums.
# A mismatch aborts the script (set -e), so a tampered or corrupted download
# never reaches /usr/local/bin.
YQ_VERSION=v4.44.3
SOPS_VERSION=v3.9.1
arch="$(dpkg --print-architecture)"
case "$arch" in
  amd64)
    YQ_SHA256=a2c097180dd884a8d50c956ee16a9cec070f30a7947cf4ebf87d5f36213e9ed7
    SOPS_SHA256=cd795109851c3a483bbaa66d15d19ddb2227ac0352b39e25d96b67d51edb6225
    ;;
  arm64)
    YQ_SHA256=0e7e1524f68d91b3ff9b089872d185940ab0fa020a5a9052046ef10547023156
    SOPS_SHA256=bc946fef11dbe199587adac567037b69374c4202f928ca138443539efc85b357
    ;;
  *)
    echo "unsupported architecture: $arch (expected amd64 or arm64)" >&2
    exit 1
    ;;
esac

install_verified() {
  # install_verified <url> <sha256> <dest>: download to a temp file, verify the
  # checksum, then install atomically. Runs under set -euo pipefail.
  local url="$1" want="$2" dest="$3" tmp
  tmp="$(mktemp)"
  curl -1sLf -o "$tmp" "$url"
  echo "${want}  ${tmp}" | sha256sum -c -
  sudo install -m 0755 "$tmp" "$dest"
  rm -f "$tmp"
}

if ! command -v yq >/dev/null 2>&1; then
  install_verified \
    "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${arch}" \
    "$YQ_SHA256" /usr/local/bin/yq
fi
if ! command -v sops >/dev/null 2>&1; then
  install_verified \
    "https://github.com/getsops/sops/releases/download/${SOPS_VERSION}/sops-${SOPS_VERSION}.linux.${arch}" \
    "$SOPS_SHA256" /usr/local/bin/sops
fi

# Caddy config layout: a base Caddyfile that imports per-service vhosts.
sudo install -d -m 0755 ` + ConfDir + `
sudo systemctl enable caddy
`

// InstallScript returns the host install script for the Caddy realization. It
// installs Caddy plus jq/yq/sops/age and prepares the conf.d directory; the
// caller writes the base Caddyfile and per-service vhosts separately and then
// reloads Caddy.
func InstallScript() string {
	return installScript
}
