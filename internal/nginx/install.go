package nginx

// installScript installs nginx plus the native ACME module from the official
// nginx.org apt repository. It is idempotent: re-running it on a provisioned host
// is a no-op beyond apt's own bookkeeping.
//
// It installs no secret tooling. Secrets are fetched at runtime by the Go
// inforge-agent, which decrypts the host-key credential in-process — the host
// needs no jq/yq/sops/age. Only the packages nginx's apt repository requires
// (curl, gnupg, ca-certificates, the distro keyring) are installed.
//
// The apt ordering is deliberate: the FIRST apt-get call is `apt-get update`,
// never `apt-get install`, so a fresh image under `set -euo pipefail` does not
// abort on a missing index. nginx-module-acme ships from the mainline repo
// (nginx ≥ 1.29.1), so the mainline channel is used. Commands run with sudo
// because inforge connects as the host's (sudo-capable) deploy user, not root.
const installScript = `#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# update BEFORE any install (the apt-order fix).
sudo -E apt-get -o DPkg::Lock::Timeout=300 update
sudo -E apt-get -o DPkg::Lock::Timeout=300 install -y curl gnupg2 ca-certificates lsb-release

# Identify the distro for the nginx.org repository path and keyring package.
. /etc/os-release
case "${ID}" in
  debian) sudo -E apt-get -o DPkg::Lock::Timeout=300 install -y debian-archive-keyring ;;
  ubuntu) sudo -E apt-get -o DPkg::Lock::Timeout=300 install -y ubuntu-keyring ;;
  *) echo "unsupported distro for nginx.org packages: ${ID}" >&2; exit 1 ;;
esac

# Official nginx.org signing key + mainline apt repository (mainline carries the
# nginx-module-acme package, which needs nginx >= 1.29.1). Download then dearmor to
# a UNIQUE temp in the keyrings dir and atomically mv into place: writing the final
# keyring directly would leave a partial file if curl dies mid-stream (the guard
# then treats it as valid forever), and a host that runs BOTH the ingress and the
# mesh nginx install concurrently would otherwise have the two dearmor writes race
# the same path. The atomic mv makes the last writer win with a complete keyring.
if [ ! -f /usr/share/keyrings/nginx-archive-keyring.gpg ]; then
  asc=$(mktemp)
  curl -fsSL https://nginx.org/keys/nginx_signing.key -o "$asc"
  tmpkey=$(sudo mktemp /usr/share/keyrings/nginx-archive-keyring.gpg.XXXXXX)
  sudo gpg --dearmor -o "$tmpkey" "$asc"
  sudo mv "$tmpkey" /usr/share/keyrings/nginx-archive-keyring.gpg
  rm -f "$asc"
fi
echo "deb [signed-by=/usr/share/keyrings/nginx-archive-keyring.gpg] http://nginx.org/packages/mainline/${ID} ${VERSION_CODENAME} nginx" \
  | sudo tee /etc/apt/sources.list.d/nginx.list >/dev/null

sudo -E apt-get -o DPkg::Lock::Timeout=300 update
sudo -E apt-get -o DPkg::Lock::Timeout=300 install -y nginx nginx-module-acme

# ACME state (account key + issued certs) must survive reloads.
sudo install -d -m 0700 -o nginx -g nginx ` + acmeStatePath + `
sudo systemctl enable nginx
`

// InstallScript returns the host install script for the nginx ingress proxy: it
// installs nginx plus the ACME module from nginx.org and prepares the ACME state
// directory. The caller writes the rendered nginx.conf and reloads separately.
func InstallScript() string {
	return installScript
}
