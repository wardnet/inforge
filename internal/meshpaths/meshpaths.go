// Package meshpaths is the dependency-free (stdlib-only) source of truth for the
// on-host names and ports the east-west service mesh proxy uses (ADR-0032). It is
// shared by the deploy side (rendering the mesh nginx config, the firewall, and the
// systemd unit) and the on-host agent, so both agree on them byte-for-byte — the
// same role internal/hostpaths plays for the runtime PEM dir and service unit.
//
// The mesh proxy is a SECOND per-host nginx, separate from the north-south one
// (ingress/gateway): splitting them by trust direction keeps the mesh — which holds
// the co-located services' leaf keys and makes the east-west authz decisions — out
// of the internet-facing process. On a regional host the mesh proxy binds only
// loopback (egress) and the private network (peer mTLS); only the global host adds a
// public mesh-gateway listener.
package meshpaths

// Mesh proxy ports and on-host locations.
const (
	// MTLSPort is the private-network port a host's mesh proxy accepts incoming peer
	// mTLS connections on. A caller's mesh dials <peer-private-ip>:MTLSPort; the
	// firewall opens it only to the network CIDR on regional hosts, and additionally
	// to the public internet on the global host (the mesh gateway). It binds the host
	// interface, not loopback, so it never collides with a service's loopback backend.
	MTLSPort = 8443

	// EgressBase is the first 127.0.0.1 port the mesh proxy assigns as a service's
	// egress endpoint: INFORGE_MESH_URL points at http://127.0.0.1:<egress port>, and
	// the port identifies the calling service (v1 identity mechanism, ADR-0032). Each
	// mesh service co-located on a host gets a distinct port from
	// [EgressBase, EgressBase+MaxServices).
	EgressBase = 9500
	// MaxServices bounds the egress port range — far above any realistic per-host mesh
	// service count. A co-located backend bind (route target, mesh.port, health, exposed
	// port) must stay out of this range on a mesh host, exactly as it must avoid the
	// nginx ssl_preread loopback range on an ingress host.
	MaxServices = 256

	// UnitName is the systemd unit of the per-host mesh nginx (distinct from the
	// north-south nginx unit).
	UnitName = "wardnet-mesh"
	// ConfigPath is the mesh nginx config file, separate from the north-south nginx's.
	ConfigPath = "/etc/wardnet/mesh-nginx.conf"
	// PIDPath is the mesh nginx master pid file — distinct from the north-south nginx's
	// /run/nginx.pid, since two nginx instances cannot share a pid file.
	PIDPath = "/run/wardnet-mesh-nginx.pid"
)

// DNSName is the DNS-safe name a mesh leaf carries as a DNS SAN (alongside its
// canonical SPIFFE URI SAN), so stock nginx can select the callee's server cert by
// SNI and hostname-verify a peer on the mTLS hop (ADR-0032). It is <service>.<scope>.mesh
// — e.g. "tenants.us-east-1.mesh" or "tenants.global.mesh". The identity a peer
// authorizes on remains the CN / URI SAN (<scope>/<service>); this name is purely the
// nginx handle (proxy_ssl_name / server_name), never resolved via real DNS.
func DNSName(scope, service string) string {
	return service + "." + scope + ".mesh"
}

// EgressPort returns the loopback egress port for the index-th mesh service on a host
// (0-based). It is deterministic so the deploy side and the rendered config agree on
// which port maps to which service. Callers must keep index < MaxServices.
func EgressPort(index int) int { return EgressBase + index }

// InReservedEgressRange reports whether a port falls in the mesh egress range, so a
// co-located backend bind can be validated to avoid it (a service that binds an egress
// port would shadow the mesh proxy's egress listener).
func InReservedEgressRange(port int) bool {
	return port >= EgressBase && port < EgressBase+MaxServices
}
