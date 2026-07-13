package nginx

import (
	"fmt"

	"github.com/wardnet/inforge/internal/aptlock"
)

// InstallScript returns the host install script for the nginx ingress proxy: it
// installs nginx plus the native ACME module from the official nginx.org apt
// repository and prepares the ACME state directory. The caller writes the rendered
// nginx.conf and reloads separately. It is idempotent — re-running on a provisioned
// host is a no-op beyond apt's own bookkeeping.
//
// apt invocations pass DEBIAN_FRONTEND=noninteractive on the sudo command line (NOT
// via `sudo -E`, which many sudoers configs ignore — "sudo: preserving the entire
// environment is not supported, '-E' is ignored" — leaving apt to fall back to a
// Teletype debconf frontend). `apt-get update` runs through aptlock.UpdateCmd so a
// held lists lock (a sibling install or the apt-daily timer) is retried, and the
// FIRST apt call is an update so a fresh image never installs against a missing
// index. The nginx.org signing key is installed ARMORED (apt's signed-by accepts an
// .asc), so there is no `gpg --dearmor` step (which fails on a host with no
// controlling tty). nginx-module-acme ships from the mainline channel (nginx ≥
// 1.29.1).
func InstallScript() string {
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

# Refresh the index (retrying a held lists lock) BEFORE any install.
%[1]s
sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y curl ca-certificates lsb-release

# Identify the distro for the nginx.org repository path and keyring package.
. /etc/os-release
case "${ID}" in
  debian) sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y debian-archive-keyring ;;
  ubuntu) sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y ubuntu-keyring ;;
  *) echo "unsupported distro for nginx.org packages: ${ID}" >&2; exit 1 ;;
esac

# Official nginx.org signing key (ARMORED) + mainline apt repository. apt's
# signed-by reads the .asc directly, so no gpg dearmor is needed. Download to a
# temp then install into place, so a curl failure never leaves a partial keyring
# that the presence guard would trust forever. A concurrent ingress+mesh install
# writes the same complete key, so the last writer wins with a valid file.
if [ ! -f /usr/share/keyrings/nginx-archive-keyring.asc ]; then
  asc=$(mktemp)
  curl -fsSL https://nginx.org/keys/nginx_signing.key -o "$asc"
  sudo install -m 0644 "$asc" /usr/share/keyrings/nginx-archive-keyring.asc
  rm -f "$asc"
fi
echo "deb [signed-by=/usr/share/keyrings/nginx-archive-keyring.asc] http://nginx.org/packages/mainline/${ID} ${VERSION_CODENAME} nginx" \
  | sudo tee /etc/apt/sources.list.d/nginx.list >/dev/null

%[1]s
sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y nginx nginx-module-acme

# ACME state (account key + issued certs) must survive reloads.
sudo install -d -m 0700 -o nginx -g nginx %[2]s

# The north-south nginx runs under the PACKAGED unit from nginx.org, which carries NO
# Restart= directive at all — so systemd defaults to Restart=no and a dead nginx master
# STAYS DEAD. This is the most exposed daemon in the fleet: every app FQDN, gateway route,
# service route and health listener on the host is served by it, so a permanent death here
# is a total edge outage until a human intervenes.
#
# We do not own that unit file (an apt upgrade would overwrite it), so the policy goes in a
# drop-in. Same three directives, and for the same reasons, as every unit inforge writes
# itself — see .agents/rules/daemon-units-restart-on-any-exit.md.
sudo install -d -m 0755 /etc/systemd/system/nginx.service.d
sudo tee /etc/systemd/system/nginx.service.d/10-restart.conf >/dev/null <<'UNIT'
# Managed by inforge — a daemon has no correct exit.
[Unit]
StartLimitIntervalSec=0

[Service]
Restart=always
RestartSec=5
UNIT
sudo systemctl daemon-reload
sudo systemctl enable nginx
`, aptlock.UpdateCmd(), acmeStatePath)
}
