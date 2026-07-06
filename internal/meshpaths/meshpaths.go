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
	// AgentDir is the mesh proxy's persistent, on-host config directory: it holds
	// the mesh descriptor (descriptor.yaml, secret-free — just the co-located
	// Services list, written by deploy) and the host-key-encrypted, renew-owned
	// leaf material (leaf.age — every co-located service's leaf + the shared
	// trust bundle, written by `inforge pki renew`'s SSH push, ADR-0035). Unlike a
	// service, the mesh proxy never gets a deploy-owned secrets.age: deploy has
	// no leaf material to give it. The persistent sibling of the tmpfs RuntimeDir.
	AgentDir = "/etc/wardnet/mesh"
	// DescriptorPath / LeafPath are the mesh descriptor and leaf-material files
	// within AgentDir — the same basenames the agent expects in any descriptor dir.
	DescriptorPath = AgentDir + "/descriptor.yaml"
	LeafPath       = AgentDir + "/leaf.age"
	// PIDPath is the mesh nginx master pid file — distinct from the north-south nginx's
	// /run/nginx.pid, since two nginx instances cannot share a pid file.
	PIDPath = "/run/wardnet-mesh-nginx.pid"

	// RuntimeDir is the tmpfs directory the mesh proxy reads its material from. The
	// leaf-custody shift (ADR-0032) puts each co-located service's leaf HERE — the mesh
	// presents it (server cert on the ingress hop, client cert on egress), so the
	// service itself no longer holds cert material. BundlePath is the shared mesh CA
	// bundle the proxy verifies peers against.
	RuntimeDir = "/run/wardnet/mesh"
	// BundlePath is the shared mesh CA trust bundle (ssl_client_certificate on ingress,
	// proxy_ssl_trusted_certificate on egress).
	BundlePath = RuntimeDir + "/bundle.crt"
)

// LeafCertPath / LeafKeyPath are where the mesh proxy reads a co-located service's leaf
// (keyed by the bare service name). The mesh holds these, not the service (ADR-0032).
func LeafCertPath(service string) string { return RuntimeDir + "/" + service + "/leaf.crt" }
func LeafKeyPath(service string) string  { return RuntimeDir + "/" + service + "/leaf.key" }

// LeafCertFile / LeafKeyFile are the leaf material file basenames — the provider
// secret names within a service's per-host dir and the on-host file names.
const (
	LeafCertFile = "leaf.crt"
	LeafKeyFile  = "leaf.key"
)

// LeafCertKey / LeafKeyKey / BundleKey are the provider secret keys of a mesh host's
// material, relative to the host's provider path (/<hostKey>). They are the single
// source both sides of the pull agree on: `inforge pki renew` (and the deploy
// baseline) writes them, and the mesh projection (`inforge-agent mesh-project`)
// fetches each and writes it under RuntimeDir at the SAME relative path — so
// RuntimeDir+"/"+LeafCertKey(svc) == LeafCertPath(svc) by construction (ADR-0033).
func LeafCertKey(service string) string { return service + "/" + LeafCertFile }
func LeafKeyKey(service string) string  { return service + "/" + LeafKeyFile }

// BundleKey is the provider secret key of the host's shared trust bundle.
const BundleKey = "bundle.crt"

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

// GatewayMember is the north-south gateway's mesh member name — the segment in its
// leaf identity (<scope>/gateway), its material paths (LeafCertPath("gateway")), and
// its provider keys. It is a FIXED, reserved member: never part of the positional
// service egress-port math (inserting it there would shift every co-located
// service's already-injected INFORGE_MESH_URL).
const GatewayMember = "gateway"

// GatewayEgressPort is the gateway's loopback egress listener — the port the
// north-south gateway nginx proxies daemon requests into the mesh through. It sits
// just past the positional service range so it can never collide with (or shift)
// a service's EgressPort(index).
const GatewayEgressPort = EgressBase + MaxServices

// InReservedEgressRange reports whether a port falls in the mesh egress range
// (the positional service slots plus the gateway's fixed slot), so a co-located
// backend bind can be validated to avoid it (a service that binds an egress
// port would shadow the mesh proxy's egress listener).
func InReservedEgressRange(port int) bool {
	return port >= EgressBase && port <= GatewayEgressPort
}
