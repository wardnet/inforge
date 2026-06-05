// Package caddy is the Hetzner-internal realization detail for the
// tls-termination resource: it renders the Caddy configuration (a base
// Caddyfile that imports per-service vhosts, plus one vhost per service) and
// the host install script. It is NOT a top-level inforge concept — the Hetzner
// compute provider uses it to realize a tls-termination spec over SSH. Another
// provider could realize the same spec with a managed load balancer instead and
// never touch this package.
//
// The rendering here is pure (string in, string out); the transport that writes
// these files onto a host and reloads Caddy lives in providers/hetzner.
package caddy

import (
	"fmt"

	"github.com/wardnet/inforge/internal/types"
)

// On-host paths. Caddy's default config dir is /etc/caddy; the official Debian
// package ships a unit that reads /etc/caddy/Caddyfile.
const (
	// ConfigDir is Caddy's configuration directory.
	ConfigDir = "/etc/caddy"
	// CaddyfilePath is the base Caddyfile the systemd unit loads.
	CaddyfilePath = ConfigDir + "/Caddyfile"
	// ConfDir holds one <service>.caddy vhost file per service. The base
	// Caddyfile imports every file here, so adding or removing a service is a
	// matter of writing or deleting one file and reloading.
	ConfDir = ConfigDir + "/conf.d"
)

// Caddyfile renders the base Caddyfile. It carries no sites of its own; every
// site is a per-service vhost imported from conf.d. The import glob is relative
// to ConfigDir (Caddy resolves imports relative to the importing file).
func Caddyfile() string {
	return `# Managed by inforge — do not edit by hand.
# Per-service vhosts live in conf.d/<service>.caddy and are imported below.
import conf.d/*.caddy
`
}

// VhostFilename returns the conf.d file name for a service's vhost.
func VhostFilename(service string) string {
	return service + ".caddy"
}

// VhostPath returns the absolute on-host path of a service's vhost file.
func VhostPath(service string) string {
	return ConfDir + "/" + VhostFilename(service)
}

// Vhost renders one per-service vhost. The site address is the env-scoped FQDN;
// Caddy's automatic HTTPS issues and renews an ACME certificate for it, so
// declaring ingress always terminates TLS — there is no non-TLS form. Traffic
// is reverse-proxied to the service's local port on the same host.
func Vhost(v types.Vhost) string {
	return fmt.Sprintf(`# Managed by inforge — vhost for service %q.
%s {
	reverse_proxy localhost:%d
}
`, v.Service, v.FQDN, v.Port)
}
