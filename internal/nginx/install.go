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
sudo -E apt-get update
sudo -E apt-get install -y curl gnupg2 ca-certificates lsb-release

# Identify the distro for the nginx.org repository path and keyring package.
. /etc/os-release
case "${ID}" in
  debian) sudo -E apt-get install -y debian-archive-keyring ;;
  ubuntu) sudo -E apt-get install -y ubuntu-keyring ;;
  *) echo "unsupported distro for nginx.org packages: ${ID}" >&2; exit 1 ;;
esac

# Official nginx.org signing key + mainline apt repository (mainline carries the
# nginx-module-acme package, which needs nginx >= 1.29.1).
if [ ! -f /usr/share/keyrings/nginx-archive-keyring.gpg ]; then
  curl -fsSL https://nginx.org/keys/nginx_signing.key \
    | sudo gpg --dearmor -o /usr/share/keyrings/nginx-archive-keyring.gpg
fi
echo "deb [signed-by=/usr/share/keyrings/nginx-archive-keyring.gpg] http://nginx.org/packages/mainline/${ID} ${VERSION_CODENAME} nginx" \
  | sudo tee /etc/apt/sources.list.d/nginx.list >/dev/null

sudo -E apt-get update
sudo -E apt-get install -y nginx nginx-module-acme

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
