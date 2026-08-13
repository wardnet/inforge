// Package types defines the declarative resource specs, provider interfaces,
// and the manifest contract that make up the inforge Toolkit domain.
//
// Specs are unmarshalled from a project's YAML resource files (see
// internal/loader); defaults that yaml.v3 cannot apply are normalised by the
// loader after unmarshal. Provider implementations live in the per-provider
// plugin packages and satisfy the interfaces declared here.
package types

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/meshnginx"
	"gopkg.in/yaml.v3"
)

// SubnetSpec is one subnet within a NetworkSpec.
type SubnetSpec struct {
	Name string `yaml:"name"`
	CIDR string `yaml:"cidr"`
}

// NetworkSpec is one network resource.
type NetworkSpec struct {
	Name      string       `yaml:"name"`
	Container string       `yaml:"container"`
	Provider  string       `yaml:"provider"`
	CIDR      string       `yaml:"cidr"`
	Subnets   []SubnetSpec `yaml:"subnets"`
}

// IngressSpec is one ingress resource — the shared, self-hosted proxy tier
// (nginx) that fronts apps under a domain and services (RouteSpec). It is a
// sibling of NetworkSpec (a thing other resources reference), NOT a workload: it
// references a compute Host (an FK to a compute name in the same scope, exactly
// like service.host) and reuses that host's provisioning, firewall, cloud-init
// and SSH machinery. It carries no provider field — it inherits its host's. The
// nginx/routing config it serves is NOT declared here: it is derived at deploy
// from the apps and services that reference this ingress (a service via its
// Ingress FK, contributing its Routes; an app via its Ingress FK).
type IngressSpec struct {
	Name             string `yaml:"name"`
	Container        string `yaml:"container"`
	Host             string `yaml:"host"`                         // FK -> compute resource name (same scope); reuses the host's provisioning/firewall/SSH
	HealthProbesPort int    `yaml:"health_probes_port,omitempty"` // public port nginx exposes service health checks on (defaults to 81 when omitted); opened only when a referencing service declares its own health_probes_port
	// Security opts this edge out of the env-level security tier (ADR-0043) when set to
	// false: no rate limiting (and, slice 2, no CrowdSec). Nil/absent = the env policy
	// applies. A *bool distinguishes "unset" from an explicit opt-out.
	Security *bool `yaml:"security,omitempty"`
}

// AppSpec is one front-end (static SPA) resource — a bundle served from an
// ingress under a domain. It is a sibling of ServiceSpec (a workload released
// onto a target), NOT a service subtype: an app targets an ingress, a service
// targets a host. Like a service it carries no provider field — it inherits the
// provider of the ingress (whose provider is its compute host's). The bundle
// itself is delivered at release time, not declared here.
type AppSpec struct {
	Name      string `yaml:"name"`
	Container string `yaml:"container"`
	Ingress   string `yaml:"ingress"`   // FK -> ingress resource name (same scope)
	Subdomain string `yaml:"subdomain"` // public subdomain; the FQDN is composed at realization from scope + base domain
	Spa       bool   `yaml:"spa"`       // when true, any non-file path serves index.html (SPA deep-link fallback)
}

// GatewaySpec is the north-south daemon API gateway (ADR-0032, ADR-0045): a
// PRIVATE router fronted by an ingress. It is NOT the east-west router
// (service↔service runs through the derived mesh, not here). External daemons
// HTTPS into the referenced Ingress (its public FQDN resolves to the ingress
// host); the ingress terminates TLS, enforces the edge security tier
// (CrowdSec/rate-limit, ADR-0043), and reverse-proxies the gateway FQDN over the
// private network to this gateway, preserving the client IP. A gateway is
// therefore NEVER a public edge — it opens no public port and holds no ACME cert
// of its own. Like IngressSpec it references a compute Host by name in the SAME
// scope (an FK resolved via resolveComputeHost) and carries no provider of its
// own (it inherits the host's). It is a mesh client with identity <scope>/gateway:
// a forwarded daemon request is matched against the public path globs of the
// listed Services (the routing table is DERIVED from their mesh.public_paths —
// ADR-0034: the gateway names WHICH services are public, the service names WHAT
// endpoints exist), and handed to the owning service THROUGH the mesh (so the
// gateway is location-transparent and needs no service locations). A path
// matching no public glob is answered 404 (JSON) and never traverses. It does NOT
// validate the daemon JWT — it forwards it for the service to validate.
type GatewaySpec struct {
	Name             string   `yaml:"name"`
	Container        string   `yaml:"container"`
	Host             string   `yaml:"host"`                         // FK -> compute resource name (same scope); reuses the host's provisioning/firewall/SSH
	Ingress          string   `yaml:"ingress"`                      // FK -> ingress resource (same scope) that fronts this gateway (ADR-0045). REQUIRED: a gateway is never public; the ingress terminates TLS + security and reverse-proxies to it, preserving the client IP. host and ingress.host must share a network.
	Pki              string   `yaml:"pki"`                          // FK -> two-tier (mesh) PKI in pki.enc.yaml the gateway's client leaf (<scope>/gateway) mints from (required); every listed service must join the same mesh
	Subdomain        string   `yaml:"subdomain"`                    // public subdomain; the FQDN is composed at realization from scope + base domain and resolves to the ingress host
	Services         []string `yaml:"services"`                     // FKs -> services (same scope) exposed at the edge; the routing table is derived from their mesh.public_paths
	HealthProbesPort int      `yaml:"health_probes_port,omitempty"` // public port the FRONTING INGRESS host exposes the listed services' health checks on (plain HTTP, Host-demuxed by service FQDN; defaults to 81)
	HealthProbePaths []string `yaml:"health_probe_paths,omitempty"` // exact paths nginx answers 200 "ok" directly for edge liveness (served by the gateway, reached through the ingress over the real public path); optional
	// Security opts this gateway's forwarded traffic out of the env-level rate limit
	// (ADR-0043/0045) at the fronting ingress when set to false. A gateway is never a
	// public edge, so this no longer affects CrowdSec host selection. Nil/absent = the
	// env policy applies.
	Security *bool `yaml:"security,omitempty"`
}

// EffectiveHealthProbesPort is the gateway twin of the ingress method: the
// declared public health port, or DefaultHealthProbesPort when omitted.
func (s GatewaySpec) EffectiveHealthProbesPort() int {
	if s.HealthProbesPort == 0 {
		return DefaultHealthProbesPort
	}
	return s.HealthProbesPort
}

// Port is a firewall port or port range (e.g. "80", "8000-9000"). It unmarshals
// from both YAML integers and strings so users can write `port: 80` without quotes.
type Port string

// UnmarshalYAML accepts both integer and string nodes so `port: 80` and
// `port: "8000-9000"` are both valid.
func (p *Port) UnmarshalYAML(value *yaml.Node) error {
	switch value.Tag {
	case "!!int", "!!str":
		*p = Port(value.Value)
		return nil
	default:
		return fmt.Errorf("firewall port must be an integer or string, got %s", value.Tag)
	}
}

// FirewallRule is one inbound firewall rule.
type FirewallRule struct {
	Proto string `yaml:"proto"` // "tcp" | "udp" | "icmp"
	Port  Port   `yaml:"port"`
}

// FirewallSpec declares extra inbound firewall rules for a compute resource. SSH
// (22) is always permitted, and the host's service ingress listen ports (plus :80
// when it terminates TLS) are derived and opened automatically; these Inbound
// rules are unioned on top for any non-ingress ports a host needs.
type FirewallSpec struct {
	Inbound []FirewallRule `yaml:"inbound"`
}

// ComputeSpec is one compute resource — a host runtime. Its cpus/memory are
// resolved from the size table (see internal/sizes), not declared here.
type ComputeSpec struct {
	Name          string          `yaml:"name"`
	Kind          string          `yaml:"kind"` // "vm" (default; only supported kind) | "cluster" (reserved)
	Container     string          `yaml:"container"`
	Provider      string          `yaml:"provider"`
	Network       string          `yaml:"network"`          // FK -> network spec name
	Subnet        string          `yaml:"subnet,omitempty"` // optional FK -> subnet name within the network
	Size          string          `yaml:"size"`             // resolved against the size table
	Image         string          `yaml:"image"`
	CloudInit     string          `yaml:"cloud_init,omitempty"` // sidecar filename, resolved relative to the compute's resource folder
	InstanceCount int             `yaml:"instance_count"`       // default 1; expands into specKeys name-01..name-NN
	Firewall      *FirewallSpec   `yaml:"firewall,omitempty"`
	DeployUser    *DeployUserSpec `yaml:"deploy_user,omitempty"`
}

// DnsRecord is one A-record inforge derives and creates against a region's DNS
// authority (see regions.DnsAuthority). Records are never hand-authored: they are
// derived from hosts (the "<compute>.vm" record) and from service ingress (the
// "<svc>.svc" record plus any vanity FQDNs). The FQDN is fully resolved and
// RecordName is its zone-relative form (FQDN minus the authority's zone, "@" at
// the apex) so the provider stays a pure renderer. Container labels the record's
// tags.
type DnsRecord struct {
	// Name is the unique resource-name component (e.g. "bridge-vm", "bridge-svc",
	// "key-broker-inforge") used to build the Pulumi logical name.
	Name string
	// RecordName is the zone-relative DNS name the authority appends its zone to
	// (e.g. "bridge.vm.prd.use1", or "@" at the apex).
	RecordName string
	Container  string
	Proxied    bool
}

// DatabaseClusterSpec is one database-cluster resource: a single-instance database
// engine (one initdb/postmaster) whose data lives on a single persistent volume
// (ADR-0036). Many logical databases live in one cluster, and many clusters may run
// on one host. It is deliberately single-instance — no HA/replication — despite the
// word "cluster" (PostgreSQL's own term for one initdb instance managing several
// databases). Host is a same-scope compute FK, REQUIRED when the resolved provider
// is self-hosted (inforge installs Postgres on that host) and FORBIDDEN when a
// managed provider is used. The volume size is not authored here — it is derived
// from the sizes of the cluster's logical databases (see DatabaseSpec.SizeGB).
type DatabaseClusterSpec struct {
	Name      string `yaml:"name"`
	Container string `yaml:"container"`
	Engine    string `yaml:"engine"`   // "postgresql"
	Host      string `yaml:"host"`     // same-scope compute FK (self-hosted only)
	Provider  string `yaml:"provider"` // e.g. "self-hosted"; defaults per ProviderDefaults
	Version   string `yaml:"version"`  // engine major (e.g. "17"); default per loader
}

// DatabaseSpec is one logical database inside a database-cluster (ADR-0036). It
// names its cluster by FK (same scope), the logical Database and its Owner role,
// an optional declared SizeGB (intent — Postgres has no per-database quota, so it
// contributes to its cluster's derived volume size), and an optional per-database
// Backup policy. Engine/provider/branch live on the cluster, not here.
type DatabaseSpec struct {
	Name      string       `yaml:"name"`
	Container string       `yaml:"container"`
	Cluster   string       `yaml:"cluster"`  // database-cluster FK (same scope)
	Database  string       `yaml:"database"` // the logical database name
	Owner     string       `yaml:"owner"`    // PostgreSQL role that owns the database (NOLOGIN)
	SizeGB    int          `yaml:"size_gb"`  // declared size intent; sums into the cluster volume
	Backup    BackupPolicy `yaml:"backup"`   // per-database backup policy (loader-defaulted)
	// Metrics opts a database OUT of Postgres metrics collection (ADR-0037) when set to
	// false. Nil or true = scraped by the cluster host's otelcol postgresql receiver;
	// false excludes it (no per-database metrics, no CONNECT for the monitor role).
	Metrics *bool `yaml:"metrics"`
}

// MetricsEnabled reports whether this database is scraped for Postgres metrics —
// true unless explicitly opted out with `metrics: false` (ADR-0037).
func (d DatabaseSpec) MetricsEnabled() bool {
	return d.Metrics == nil || *d.Metrics
}

// BackupPolicy is a logical database's backup configuration (ADR-0036). Enabled is a
// pointer so an absent block defaults to enabled while an explicit `enabled: false`
// opts a throwaway/derived database out. Interval is the pg_dump cadence (RPO) and
// Keep is how many backups to retain. The backup delivery itself lands in a later
// slice; this slice defines and validates the authored shape.
type BackupPolicy struct {
	Enabled  *bool  `yaml:"enabled"`  // default true when omitted
	Interval string `yaml:"interval"` // default "24h"
	Keep     int    `yaml:"keep"`     // default 7
}

// DeployUserSpec configures the deploy user provisioned on a compute instance
// at VM-init time. The SSH key material comes from SSHConfig.DeployPublicKey.
type DeployUserSpec struct {
	Name string `yaml:"name"`
}

// RouteSpec is one typed inbound routing entry a service exposes through its
// ingress (the standalone proxy tier the service's Ingress FK names — ADR-0026).
// Every route is fronted by the ingress nginx — there is no direct-bind path: the
// ingress is the sole public entry point, and the service binds Target behind it.
// nginx reverse-proxies/forwards to the backend over loopback when the service is
// co-located with the ingress host (127.0.0.1:Target) or over the private network
// when they are different hosts (<private-ip>:Target). Each route binds a public
// Listen port (which must differ from Target when co-located, since nginx occupies
// that port on all interfaces of the shared host), served per Type:
//
//   - "tls-termination": nginx terminates ACME TLS for the service's SNIs (the
//     auto-derived "<svc>.svc" FQDN plus any Vanity entries) and reverse-proxies
//     cleartext to the backend Target. Multiple services on one ingress may share
//     one Listen port (nginx demuxes by SNI/server_name).
//   - "forward": nginx stream-forwards the raw L4 connection to the backend Target
//     with the PROXY protocol (so the backend learns the client address); the
//     backend owns its own TLS. A forward Listen is single-service-exclusive.
//
// A truly raw public port (no proxy) is not a route — declare it on the compute's
// firewall.inbound instead. The env-scoped FQDNs are derived at realization time
// (see naming.ServiceFQDN / naming.ExpandVanity), not authored here.
type RouteSpec struct {
	Type   string `yaml:"type"`   // "tls-termination" | "forward"
	Listen int    `yaml:"listen"` // public port the ingress nginx accepts traffic on (required; != Target when co-located)
	Target int    `yaml:"target"` // backend port the service listens on (required)
	// Vanity adds extra public FQDNs (beyond the auto-derived "<svc>.svc" name) a
	// tls-termination route serves: a bare token is env+region-scoped, anything
	// with a dot or a {BASE_DOMAIN}/{ENV}/{REGION_SLUG} placeholder is a literal
	// FQDN. Valid only on tls-termination (forward has no SNI).
	Vanity []string `yaml:"vanity,omitempty"`
}

// Route (ingress) entry types.
const (
	IngressTypeTLSTermination = "tls-termination"
	IngressTypeForward        = "forward"
)

// MeshSpec is a service's east-west mesh membership surface (ADR-0032). A service
// is a mesh member by declaring pki:; this optional block declares what it exposes
// to peers: the loopback Port it serves mesh traffic on (plain HTTP — the local
// mesh proxy forwards verified peer traffic to 127.0.0.1:Port), and AllowedServices,
// the callee-side allow list of who may call it (bare service names, enforced at
// this service's local mesh; include "gateway" to be reachable by daemon traffic
// through the north-south gateway). A pki: service with no mesh block can still make
// outbound mesh calls (INFORGE_MESH_URL) but exposes nothing inbound.
type MeshSpec struct {
	Port            int      `yaml:"port"`                     // loopback backend port this service serves mesh traffic on (plain HTTP)
	AllowedServices []string `yaml:"allowed_services"`         // bare service names permitted to call this service over the mesh
	PublicPaths     []string `yaml:"public_paths,omitempty"`   // absolute path globs (ADR-0034: * = one segment, trailing /** = any tail) exposed at the internet edge through a gateway that lists this service; also admitted from mesh peers
	InternalPaths   []string `yaml:"internal_paths,omitempty"` // absolute path globs admitted from mesh peers only — never served by a gateway; a callee must declare >=1 path across both lists (its endpoint surface is allowlist-only)
}

// DefaultHealthProbesPort is the public port an ingress exposes service health
// checks on when its manifest omits health_probes_port.
const DefaultHealthProbesPort = 81

// EffectiveHealthProbesPort is the single source of truth for the public health
// port an ingress exposes: its declared health_probes_port, or DefaultHealthProbesPort
// when omitted. The loader normalizes the field to this value, but validation and the
// deploy program also call it so a spec built without the loader still resolves
// correctly.
func (s IngressSpec) EffectiveHealthProbesPort() int {
	if s.HealthProbesPort == 0 {
		return DefaultHealthProbesPort
	}
	return s.HealthProbesPort
}

// GrantSpec is one entry in a service's grants: list — a declared, permissioned
// access to a Grantable resource (a Database or a PKI resource), materialized as
// the env vars in Outputs (ADR-0025). It is topological — it wires the service to
// a named resource — so it lives on the manifest beside pki:/ingress:, not in the
// environment.yaml Source DSL (which only reads existing outputs). Distinct from
// mesh pki: membership (intrinsic identity, not a granted permission).
type GrantSpec struct {
	// Resource is the granted resource as "<type>/<name>", e.g. "database/main"
	// or "pki/daemon" (type ∈ database | pki). A global target uses a "global/"
	// name prefix (e.g. "database/global/main").
	Resource string `yaml:"resource"`
	// Permission is "ro" or "rw"; each Grantable maps it to its domain (DB
	// read-only/read-write user; PKI verify/issue).
	Permission string `yaml:"permission"`
	// Outputs maps an env-var name to a template over {FIELD} placeholders scoped
	// to this grant. A value-field template composes a string secret; a file-field
	// placeholder resolves to the projected PEM's on-host path.
	Outputs map[string]string `yaml:"outputs"`
}

// ServiceSpec is one service resource — a workload hosted on a compute.
type ServiceSpec struct {
	Name             string            `yaml:"name"`
	Container        string            `yaml:"container"`
	Host             string            `yaml:"host"`                         // FK -> bare compute name (e.g. "bridge", not "bridge-01"); kind must be vm
	Type             string            `yaml:"type"`                         // "raw" (built) | "container" (reserved)
	User             string            `yaml:"user,omitempty"`               // no-login system user the service runs as; raw only
	Pki              string            `yaml:"pki"`                          // FK -> two-tier (mesh) PKI name in pki.enc.yaml this service is a leaf member of (required)
	MtlsFiles        bool              `yaml:"mtls_files,omitempty"`         // opt-in: also project this service's own leaf + trust bundle into its tmpfs and inject MTLS_*_PATH — for a raw mTLS plane outside the mesh (e.g. tunneller node↔node); default false = the mesh proxy is the sole leaf custodian (ADR-0033)
	Reload           string            `yaml:"reload,omitempty"`             // optional ExecReload command to apply a renewed mesh leaf without downtime (e.g. "/bin/kill -HUP $MAINPID"); absent -> renewal restarts the unit
	Ingress          string            `yaml:"ingress,omitempty"`            // FK -> ingress resource name (same scope) whose nginx fronts this service's Routes; required when Routes is non-empty
	Routes           []RouteSpec       `yaml:"routes,omitempty"`             // typed inbound routes (tls-termination / forward) realized on the referenced ingress's nginx
	HealthProbesPort int               `yaml:"health_probes_port,omitempty"` // backend port this service serves health checks on; surfaced through the ingress's (or, for a gateway-listed service without ingress, the gateway's) public health port, Host-demuxed by the service FQDN
	HealthProbePaths []string          `yaml:"health_probe_paths,omitempty"` // exact request paths the health server proxies to HealthProbesPort — anything else 404s; required (>=1) when HealthProbesPort is set (allowlist-only, ADR-0034)
	ExposedPorts     []ExposedPort     `yaml:"exposed_ports,omitempty"`      // ports the service binds that inforge opens on the host's private network only (never the public internet), for peer/service-to-service traffic; needs no ingress (ADR-0029)
	Mesh             *MeshSpec         `yaml:"mesh,omitempty"`               // east-west mesh exposure: the loopback port peers reach + the callee-side allow list (ADR-0032)
	Grants           []GrantSpec       `yaml:"grants,omitempty"`             // permissioned access to Grantable resources (database/pki), materialized as env vars (ADR-0025)
	Environment      map[string]string `yaml:"-"`                            // env-var-name → source DSL string (ref:, vault:KEY, env:VAR, or literal); loaded from the service's sibling environment.yaml, not the manifest; the secrets provider is derived from the region, not the service
}

// ExposedPort is one private-network port a service binds (ADR-0029): inforge opens
// it on the host's private-network CIDR only, never the public internet. It is the
// private sibling of compute.firewall.inbound (which is public). Unlike that rule,
// the port is a plain integer (no ranges) and proto is tcp/udp only (no icmp), so it
// is comparable and usable directly as a map key.
type ExposedPort struct {
	Proto string `yaml:"proto"` // "tcp" | "udp"
	Port  int    `yaml:"port"`  // 1..65535
}

// PKIResourceSpec is one PKI resource — a root-only Certificate Authority that
// services obtain cert material from via a Grant (ADR-0025). It is distinct from
// the mesh-auth PKI store (the env-root pki.enc.yaml, two-tier, consumed via the
// service pki: membership field): a Grant may target only a PKI resource, and
// pki: membership may name only a mesh PKI. Like every resource it carries a scope
// from its folder — regional/pki/<name> is instantiated per region (one
// independent root each); global/pki/<name> is region-less.
//
// The manifest only DECLARES that the CA should exist. Its material (root cert +
// age-encrypted root key) is generated separately, by `inforge pki add <env>
// <name> --topology root-only`, into the environment's pki.enc.yaml — and a grant
// on a declared-but-ungenerated PKI is rejected by validate.checkGrants, since it
// would have nothing to deliver.
type PKIResourceSpec struct {
	Name      string `yaml:"name"`
	Container string `yaml:"container"`
	Topology  string `yaml:"topology"`           // "root-only" — the only valid topology for a PKI resource
	Validity  string `yaml:"validity,omitempty"` // optional CA validity (e.g. "10y")
}

// RateLimitProfile is the resolved IP-based rate limit applied to an edge's public
// servers (ADR-0043). Rate limiting is a blanket ingress SECURITY measure, not per-route
// tuning: one limit is derived at deploy from the env's security.rate_limit block and
// stamped UNIFORMLY on every server of an edge (per-route / per-identity limits are a
// gateway-module concern — ADR-0044 — not this layer). It rides on the ingress-derived
// server structs (IngressRoute/IngressApp/IngressGateway) so the nginx renderer emits the
// shared-memory zone and the per-server limit directives without a wider signature; a nil
// pointer means the edge has no rate limiting (disabled, or opted out via `security:
// false`). Name is the nginx zone stem (a fixed constant, since the limit is uniform).
// Keying is always the client IP (ADR-0043); RPS/Burst drive limit_req (http only —
// ignored for an L4 forward), MaxConn drives limit_conn (http and stream).
type RateLimitProfile struct {
	Name    string // nginx zone stem (a fixed constant — the limit is edge-uniform)
	RPS     int    // requests/second (limit_req rate); 0 disables request-rate limiting
	Burst   int    // queued excess before a 429 (limit_req burst)
	MaxConn int    // max concurrent connections per client IP (limit_conn); 0 disables
}

// IngressRoute is one typed inbound routing entry the ingress proxy (nginx)
// realizes, derived from one route of one service that references the ingress.
// The ingress is the sole public entry point: nginx fronts the service on the
// public Listen port and proxies to the backend at Backend:Target. FQDNs are
// fully resolved (env + region slug + base domain) and Backend is a literal
// address before they reach the provider, so the provider stays a pure
// renderer/installer and never re-derives names or resolves IPs.
//
// Type selects how the Listen port is served:
//   - "tls-termination": nginx terminates ACME TLS for FQDNs (server_name demux)
//     and reverse-proxies cleartext to Backend:Target. Several routes may share
//     one Listen port, distinguished by FQDNs.
//   - "forward": nginx stream-forwards the raw L4 connection on Listen to
//     Backend:Target with the PROXY protocol. FQDNs is empty (no SNI).
//
// Backend is the address nginx proxies to: "127.0.0.1" when the service is
// co-located with the ingress host, or the backend host's private IP when they
// are different hosts. The program leaves Backend empty for a cross-host route and
// the provider fills it from the resolved backend private-IP output before Render.
type IngressRoute struct {
	Service string
	Type    string   // IngressTypeTLSTermination | IngressTypeForward
	FQDNs   []string // fully-qualified, env-scoped SNIs (tls-termination only; nil for forward)
	Listen  int      // public port the ingress accepts traffic on
	Target  int      // backend port the service listens on
	Backend string   // backend address nginx proxies to ("127.0.0.1" co-located; private IP cross-host)
	// RateLimit is the resolved rate-limit profile for this route (nil = none). A
	// tls-termination route uses RPS/Burst (limit_req) and MaxConn (limit_conn); a
	// forward route uses only MaxConn (stream limit_conn — L4 has no request rate).
	RateLimit *RateLimitProfile
}

// IngressApp is one static front-end (SPA) the ingress proxy (nginx) serves from
// disk, derived from one app resource that references the ingress (ADR-0026,
// slice C). Unlike an IngressRoute it has no backend: nginx terminates ACME TLS
// for the app's single FQDN and serves files from Root (the on-host document
// root, an inforge-managed `current` symlink the release path swaps). FQDN is the
// clean dotted app form (naming.AppFQDN), fully resolved (scope slug + base
// domain) before it reaches the provider, so the provider stays a pure
// renderer/installer. Spa selects the deep-link fallback: any non-file path
// serves index.html when true, else a 404.
type IngressApp struct {
	Name string // app resource name (used to name the Pulumi command resources)
	FQDN string // fully-qualified app domain (single SNI / ACME cert)
	Root string // on-host document root nginx serves (the `current` symlink)
	Spa  bool   // true -> try_files fallback to /index.html (SPA deep links)
	// RateLimit is the resolved rate-limit profile for this app's server (nil = none).
	RateLimit *RateLimitProfile
}

// IngressHealth is one service health endpoint the ingress proxy (nginx) surfaces,
// derived from a service that references the ingress and declares a
// HealthProbesPort. Unlike a route it is plain HTTP (no TLS): every health entry
// on a host shares the ingress's single public health port and is demuxed strictly
// by FQDN (the request Host header / server_name), then reverse-proxied to the
// service's backend health port. FQDN is the service's canonical naming.ServiceFQDN
// (a forward-only service still has this derived name); Backend is resolved like a
// route's ("127.0.0.1" co-located, the backend's private IP cross-host) before it
// reaches the provider; Target is the backend health port.
type IngressHealth struct {
	Service string
	FQDN    string   // canonical service FQDN matched as server_name / Host
	Target  int      // backend port the service serves health checks on
	Backend string   // backend address nginx proxies to ("127.0.0.1" co-located; private IP cross-host)
	Paths   []string // exact request paths proxied to the backend; anything else 404s (ADR-0034)
}

// IngressGateway is the north-south daemon gateway server the public nginx
// realizes on a host (ADR-0032/0034): one TLS server on the gateway's FQDN whose
// regex locations (compiled from the listed services' public path globs) hand
// daemon requests to the LOCAL mesh proxy's gateway egress listener
// (meshpaths.GatewayEgressPort), naming the owning service in X-Mesh-Target.
// The path is preserved byte-for-byte (daemon PoP signs it) and the daemon's
// Authorization header is forwarded untouched — the service, not the gateway,
// validates the JWT. A path matching no route is answered 404 (JSON) at the
// edge. Like an IngressApp it has one FQDN and no resolved backend address:
// location is the mesh's business.
type IngressGateway struct {
	Name             string // gateway resource name (used to name Pulumi command resources)
	FQDN             string // fully-qualified gateway domain (single SNI / ACME cert on the fronting ingress)
	Routes           []IngressGatewayRoute
	HealthProbePaths []string // exact paths the gateway's routing server answers 200 "ok" directly (edge liveness, reached through the ingress)
	// RateLimit is the resolved rate-limit profile applied to this gateway's termination
	// server on the ingress (nil = none). The edge health-probe locations are never limited.
	RateLimit *RateLimitProfile
	// Model-A private-gateway wiring (ADR-0045). A gateway is fronted by an ingress:
	// the ingress renders a TERMINATION server (TLS + security → proxy to the gateway),
	// and the gateway host renders a plain-HTTP ROUTING server (real_ip recovery →
	// mesh egress). These fields carry the resolved addresses that connect the two.
	//
	// Backend is the address the ingress termination server proxies to — "127.0.0.1"
	// when the gateway is co-located with its ingress, else the gateway host's private
	// IP. HTTPPort is the routing server's plain-HTTP port (nginx.GatewayHTTPPort).
	// RealIPFrom is the source the routing server trusts for X-Forwarded-For —
	// "127.0.0.1" co-located, else the ingress host's private IP. ListenAddr is the
	// routing server's bind address — "127.0.0.1" co-located (only the local ingress
	// reaches it), else the gateway host's OWN private IP (never the public interface;
	// left empty for the provider to fill from the host being realized).
	Backend    string
	HTTPPort   int
	RealIPFrom string
	ListenAddr string
}

// IngressGatewayRoute is one derived path route on the gateway server: daemon
// requests matching Pattern (a raw pathglob the renderer compiles to a regex
// location) are handed to Service through the mesh.
type IngressGatewayRoute struct {
	Pattern string // raw path glob from the owning service's mesh.public_paths
	Service string // target service name (the X-Mesh-Target value)
}

// NetworkOutputs are the values a NetworkProvider returns after creating a
// network, consumed by the compute provider.
type NetworkOutputs struct {
	NetworkID pulumi.StringOutput
	SubnetID  pulumi.StringOutput
}

// ComputeOutputs are the values a ComputeProvider returns after creating a host.
// PrivateIP is the host's address on its attached private network — empty from
// Create and in preview; it is filled in by the program's post-gate attach pass from
// ComputeProvider.AttachNetwork (the private network is attached after the cloud-init
// gate, not inline — see ComputeProvider). It is used by the ingress tier to
// proxy_pass to a backend that lives on a different host within the same Hetzner
// Network (cross-host routing) — the sole consumer.
// The four metadata fields below are provider-supplied OTel resource-identity facts,
// known at plan time (plain strings, not Pulumi outputs). They are the host/cloud
// ground truth a running process cannot determine for itself; renderDescriptor reads
// them off the host's outputs and injects them as INFORGE_* env vars (ADR-0030), and
// the host metrics collector (ADR-0031) stamps the same values. A provider that does
// not supply one leaves it empty, and the empty value is omitted downstream.
type ComputeOutputs struct {
	PublicIP  pulumi.StringOutput
	PrivateIP pulumi.StringOutput

	CloudProvider    string // cloud.provider, e.g. "hetzner"
	CloudRegion      string // cloud.region — the provider's region, e.g. Hetzner network_zone "us-east"
	AvailabilityZone string // cloud.availability_zone — the datacenter, e.g. Hetzner location "ash"
	MachineType      string // host.type — the server-type SKU, e.g. "cx23"
}

// FirewallPorts is the derived inbound port plan for one host, computed by the
// program so the firewall stays a pure consumer. Public ports are opened to the
// internet (0.0.0.0/0 + ::/0) — an ingress host's route Listen ports plus :80 for
// ACME HTTP-01. Private ports are opened only to PrivateSourceCIDR (the host's
// private network CIDR) — a backend's route Target ports, reachable solely from a
// co-tenant ingress over the private network. PrivateExposed carries a service's
// exposed_ports (ADR-0029): proto-aware private binds opened to PrivateSourceCIDR
// too, never the internet. All lists are deduped and sorted.
type FirewallPorts struct {
	Public            []int
	Private           []int
	PrivateExposed    []ExposedPort // proto-aware service exposed_ports, opened only to PrivateSourceCIDR (ADR-0029)
	PrivateSourceCIDR string        // private network CIDR scoping Private/PrivateExposed; "" when both are empty
}

// DatabaseOutputs are the values a DatabaseProvider returns after creating a
// database. A database exposes NO credential-bearing ref output (ADR-0025): DB
// credentials flow only through a grant, which mints a scoped per-service role via
// RoleProvisioner. ref:database/* is rejected. RoleProvisioner is bound to this one
// database; it is threaded through AllOutputs so a grant resolves its target the
// same way ref: does, including the cross-region global/ redirect.
type DatabaseOutputs struct {
	RoleProvisioner DBRoleProvisioner
}

// DBRoleProvisioner mints a scoped per-service database role and applies its ro/rw
// privileges, returning the role's connection value fields. It is set on a
// DatabaseOutputs by the database provider's Create, bound to that one database.
// The program supplies the consumer-scoped roleName, so a regional service granting
// a global database gets its own role named for the consumer's region — two regions
// never collide. permission is "ro"|"rw" as a plain string so this interface need
// not import internal/grant.
type DBRoleProvisioner interface {
	ProvisionRole(ctx *pulumi.Context, roleName, permission string) (DBRoleFields, error)
}

// DBRoleFields are the connection value fields a database grant publishes. The
// discrete fields are the literal (decoded) values; URL is the role's full
// connection URI exactly as the provider returns it (already URL-encoded), so a
// grant template can compose a DSN with `{URL}` without re-encoding a password
// that contains URL-reserved characters.
type DBRoleFields struct {
	User     pulumi.StringOutput
	Password pulumi.StringOutput
	Host     pulumi.StringOutput
	Port     pulumi.StringOutput
	DBName   pulumi.StringOutput
	URL      pulumi.StringOutput
}

// AllOutputs collects per-region outputs so the secrets backend can resolve
// cross-resource references. Keyed by region, then specKey/name.
type AllOutputs struct {
	Compute  map[string]map[string]ComputeOutputs
	Database map[string]map[string]DatabaseOutputs
	// Encrypted maps service -> KEY -> plaintext for `vault:<KEY>` secrets,
	// decrypted once per deploy from resources/<env>/secrets.enc.yaml (ADR-0017,
	// ADR-0040 — keyed by the service that declares the reference, never by its
	// container). Region-independent — the store is env-scoped. Nil when the
	// environment declares no vault: sources.
	Encrypted map[string]map[string]string
	// PKI maps a root-only PKI resource's bare name -> its decrypted material,
	// resolved once per deploy from resources/<env>/pki.enc.yaml for every PKI a
	// service actually grants (ADR-0025 slice C). Region-independent — the store
	// is env-scoped, so a regional service's `pki/global/<name>` grant and a
	// global service's `pki/<name>` grant resolve the same entry. Nil when no
	// service declares a pki/* grant.
	PKI map[string]PKIMaterial
}

// PKIMaterial is a root-only PKI resource's grantable material: the CA
// certificate (public — committed in the clear) and its root signing key
// (decrypted from the store's age ciphertext with the CI identity). Both are
// file fields: a grant projects them as on-host PEMs and hands the service the
// PATH, never the content (see grant.PKIResource).
//
// Key is the sensitive half. It is delivered only to a `rw` (issue) grant's
// consumer, and it leaves the deploy process only as part of the age-encrypted
// payload of the target host's secrets.age.
//
// Scope is the single scope the root-only PKI serves ("global" or an abstract
// region). It is carried here so the deploy can reject a grant whose consumer sits
// in a different scope: the store is env-scoped and keyed by bare name, so without
// this check a regional service would silently receive a global PKI's root key (or
// two regions would share one root, which a per-region PKI resource must never do).
type PKIMaterial struct {
	Cert  string
	Key   string
	Scope string
}

// ResolveScoped looks up name in a region-keyed output map (Compute/Database),
// honoring the one allowed cross-region form: a "global/" name prefix redirects
// the lookup to the region-less "global" slot, independent of the consuming
// service's region. It returns the value, the resolved (region, bareName) for
// error messages, and whether it was found. This is the single source of the
// global/ redirect rule — both the Source DSL (ref:) and grant target resolution
// MUST use it so they resolve a global resource identically (ADR-0025).
// GlobalPrefix is the one allowed cross-scope reference form: a regional resource
// names a global one as "global/<name>". It is the single source of that spelling
// — ResolveScoped applies it to the region-keyed output maps, and StripGlobalPrefix
// applies it to the env-scoped stores (the PKI store), which need the bare name but
// no region redirect.
const GlobalPrefix = "global/"

// StripGlobalPrefix reduces a possibly cross-scope reference to the bare resource
// name. Use it for an ENV-SCOPED store (one file per environment, region-independent
// — e.g. pki.enc.yaml), where "global/" carries no lookup meaning and would only
// corrupt the key. A region-keyed map must use ResolveScoped instead, which performs
// the actual redirect. Both must agree on the spelling, so both come from here.
func StripGlobalPrefix(name string) string {
	return strings.TrimPrefix(name, GlobalPrefix)
}

func ResolveScoped[V any](m map[string]map[string]V, region, name string) (value V, resolvedRegion, bareName string, found bool) {
	resolvedRegion, bareName = region, name
	if rest, ok := strings.CutPrefix(name, GlobalPrefix); ok {
		resolvedRegion, bareName = "global", rest
	}
	inner, ok := m[resolvedRegion]
	if !ok {
		return value, resolvedRegion, bareName, false
	}
	value, ok = inner[bareName]
	return value, resolvedRegion, bareName, ok
}

// NetworkProvider creates a network for one spec in one region. Returns a map
// from subnet name to its outputs so callers can look up a specific subnet.
type NetworkProvider interface {
	Create(ctx *pulumi.Context, spec NetworkSpec, env, abstractRegion string) (map[string]NetworkOutputs, error)
}

// ComputeProvider creates one compute instance, wiring in its network, the host
// domain, the assembled (plain, secret-free) manifest, and the derived inbound
// firewall port plan. fw carries this host's public ports (an ingress host's route
// Listen ports plus :80 for ACME) and private ports (a backend's route Target
// ports, scoped to the private network CIDR) — the program derives them so the
// firewall stays a pure consumer. Secret delivery is no longer a compute-creation
// concern: secrets are fetched at runtime by inforge-agent, so there is no
// bootstrap document.
//
// Contract: when spec.DeployUser is set, an implementation MUST provision that
// account at first boot — create the login user and install ssh.deployPublicKey
// into its authorized_keys — so the host is SSH-reachable on that account. The
// program-level realization passes (service provisioning, observability collector
// install, app placeholder seeding, ingress install) all connect as the
// deploy_user and depend on this; a provider that omits it produces a host that
// passes preview/validate yet fails every host-level command with
// "[none publickey]". This obligation holds independently of whether the spec
// declares a cloud_init template.
//
// The private network is attached in TWO steps, not one: Create provisions the
// server WITHOUT its private network, and AttachNetwork attaches it afterward. This
// is mandatory on Hetzner + cloud-init >= 25.3 (Ubuntu 24.04.4 / 26.04), where the
// datasource configures the private NIC itself: a NIC present at first boot races
// init-local — the hot-added device is not yet enumerated when the network-config is
// processed, yielding a null-named interface that fails network-config-v1 schema
// validation and crashes cloud-init in sys_dev_path(None). That leaves a sticky
// `cloud-init status: error`, which fails the root cloud-init readiness gate (and so
// the whole deploy) before any host-level command runs. Deferring the attach until
// after the gate (first boot complete) lets the image's hotplug path configure the
// NIC cleanly; every later reboot is fine because the NIC is then a persistent device
// enumerated before init-local. See
// .agents/rules/attach-private-network-after-cloud-init-gate.md.
type ComputeProvider interface {
	Create(ctx *pulumi.Context, spec ComputeSpec, network NetworkOutputs, env, abstractRegion, domain, manifest string, fw FirewallPorts) (ComputeOutputs, error)
	// AttachNetwork attaches the private network of the server Create built for
	// spec's instance (1-based, matching Create's instance_count expansion), gated on
	// dependsOn (the host's cloud-init readiness gate), and returns the assigned
	// private IP. It MUST NOT attach the network inline in Create (see the type comment).
	AttachNetwork(ctx *pulumi.Context, spec ComputeSpec, instance int, dependsOn []pulumi.Resource) (pulumi.StringOutput, error)
}

// StorageRequest describes a persistent block volume to provision for a compute host
// (ADR-0036). It is provider-neutral: a Hetzner realization creates an hcloud.Volume;
// a future AWS realization would create an EBS volume of the same intent.
type StorageRequest struct {
	Name        string // logical volume name (the database-cluster name)
	Env         string
	Region      string // abstract region → the provider's location/zone
	Container   string // grouping label
	HostSpecKey string // naming.SpecKey of the compute instance the volume attaches to
	SizeGB      int    // requested size; the provider floors it at its own minimum
}

// StorageOutputs is the realized volume's on-host handle.
type StorageOutputs struct {
	// DevicePath is the stable on-host block-device path the volume appears at (e.g.
	// Hetzner's /dev/disk/by-id/scsi-0HC_Volume_<id>), used to mkfs/mount it on-host.
	DevicePath pulumi.StringOutput
	// Attachment is the volume-attachment resource. An on-host mkfs/mount command must
	// DependsOn it so it never runs before the device is attached to the server.
	Attachment pulumi.Resource
}

// StorageProvider provisions a persistent block volume and attaches it to a compute
// host built by the matching ComputeProvider (ADR-0036). The attach MUST be gated on
// the host's cloud-init readiness (dependsOn), exactly like ComputeProvider.AttachNetwork,
// so on-host formatting/mounting runs against a ready host. The volume is created
// unformatted so a reattached, already-populated volume is never reformatted. A
// provider floors SizeGB at its own minimum volume size.
type StorageProvider interface {
	CreateVolume(ctx *pulumi.Context, req StorageRequest, dependsOn []pulumi.Resource) (StorageOutputs, error)
}

// DnsProvider creates a derived DNS record pointing at a compute instance, on a
// region's DNS authority.
type DnsProvider interface {
	CreateRecord(ctx *pulumi.Context, rec DnsRecord, target ComputeOutputs) error
}

// IngressProvider realizes an ingress host's nginx proxy. It is invoked once per
// ingress host — the compute an ingress resource references — with the merged
// routes of every service and the apps that point at that host. hostKey is the
// canonical compute specKey of the ingress host (used to name the Pulumi command
// resources); host carries its public IP; deployUser is the sudo-capable account
// inforge connects as over SSH (the ingress host's deploy user); routes are the
// typed inbound routing entries (tls-termination / forward), with FQDNs already
// env-scoped by the caller and Backend filled for co-located routes; apps are the
// static front-ends nginx serves from disk (slice C), each with its FQDN and root
// already resolved. A host with apps but no routes still realizes — nginx is
// installed and its ACME cert provisioned for the app FQDNs alone.
//
// backendIPs maps a service name to its backend host's private-IP output, present
// only for cross-host routes (a route whose service runs on a host other than the
// ingress host). The provider renders the config inside an apply over these
// outputs, substituting each cross-host route's Backend with the resolved private
// IP; co-located routes already carry "127.0.0.1". Apps carry no backend, so they
// render synchronously. health entries resolve their backend the same way routes
// do (cross-host substituted from backendIPs); healthPort is the ingress's public
// health listener port. Realize installs nginx once per host, writes its config,
// and reloads — and must be safe to re-run as routes/apps/health change.
//
// env scopes the names of the Pulumi resources it creates. dependsOn carries the
// host's cloud-init readiness gate (and any other prerequisites): the provider must
// make its first per-host SSH command depend on it so realization never races
// deploy_user creation.
// gateways are the gateway TERMINATION servers this host fronts as an ingress
// (ADR-0045); gatewayRoutes are the gateway ROUTING servers this host runs as a
// gateway host. When split (gateway and ingress on different hosts) a cross-host
// private IP is substituted from gatewayIPs, keyed by gateway name — the gateway
// host's IP for a termination's Backend, the ingress host's IP for a routing
// server's real-IP source. Co-located gateways carry "127.0.0.1" already and need
// no entry.
type IngressProvider interface {
	Realize(ctx *pulumi.Context, hostKey string, host ComputeOutputs, deployUser string, routes []IngressRoute, apps []IngressApp, health []IngressHealth, healthPort int, gateways []IngressGateway, gatewayRoutes []IngressGateway, backendIPs map[string]pulumi.StringOutput, gatewayIPs map[string]pulumi.StringOutput, env string, dependsOn []pulumi.Resource) error
}

// MeshProvider realizes a host's east-west mesh proxy — the SECOND nginx, private
// (ADR-0032). It is invoked once per mesh host (a host running ≥1 pki: service)
// with the host's fully-derived mesh config, minus the addresses that come from
// compute outputs: cfg carries the co-located callee (Local) and caller (Egress)
// planes, the routing table (Targets, each with its SNI; Addr left empty), and
// the trust bundle path, but NOT ListenAddr (filled from listenAddr) or the
// Targets' Addr (filled from targetIPs). listenAddr is the address the mTLS
// ingress binds — the host's private IP on a regional host, the literal
// "0.0.0.0" on the global host (which additionally accepts cross-scope peers
// publicly). targetIPs maps each routing-table target service to the IP its Addr
// resolves against (the target host's private IP same-scope, or a global host's
// public IP cross-scope) — the program chooses which; the provider renders the
// config inside an apply over these outputs and MTLSPort. hostKey names the
// Pulumi command resources; dependsOn carries the host's cloud-init gate. Realize
// installs the mesh nginx (a separate unit + config from the north-south nginx),
// seeds placeholder cert material so it starts before real leaves land, writes
// the config, and reloads — safe to re-run as the mesh topology changes.
type MeshProvider interface {
	// Realize returns the resource that makes the host's mesh config LIVE — the nginx
	// reload, not the config write (which only puts bytes on disk).
	//
	// It is returned so a service restart can be ordered AFTER it. Pulumi is a DAG, not a
	// script: without an explicit edge, a caller's restart and a callee's allow-map reload
	// run CONCURRENTLY, and a caller that comes up first is 403'd by the stale allow-map.
	// See .agents/rules/mesh-config-lands-before-callers-restart.md.
	//
	// A provider that realizes nothing may return (nil, nil) — a no-op mesh imposes no
	// ordering. Callers must therefore nil-check before collecting the result: a nil
	// Resource reaching pulumi.DependsOn nil-derefs for every service that depends on it.
	Realize(ctx *pulumi.Context, hostKey string, host ComputeOutputs, deployUser string, cfg meshnginx.Config, listenAddr pulumi.StringInput, targetIPs map[string]pulumi.StringOutput, env string, dependsOn []pulumi.Resource) (pulumi.Resource, error)
}

// DatabaseProvider realizes a database-cluster and its logical databases on a
// managed backend, returning one DatabaseOutputs (carrying a bound DBRoleProvisioner)
// per logical database keyed by the database resource name. It is the retained
// managed seam (ADR-0036): the self-hosted path does NOT go through it (it realizes
// the cluster on a compute host directly and constructs its own on-host
// DBRoleProvisioner), but the interface stays so a managed provider (a future Neon
// re-add, RDS, …) plugs back in without touching grants. No provider is registered
// today — registry.Database returns an unknown-provider error for every name.
type DatabaseProvider interface {
	Create(ctx *pulumi.Context, cluster DatabaseClusterSpec, databases []DatabaseSpec, env, region string) (map[string]DatabaseOutputs, error)
}

// ManifestContribution is a set of non-secret fields a contributor adds to a
// compute instance's manifest.
type ManifestContribution = map[string]any

// SSHConfig holds the per-environment SSH material applied to hosts.
type SSHConfig struct {
	AuthorizedKeys  string `yaml:"authorizedKeys"`
	DeployPublicKey string `yaml:"deployPublicKey"`
	// DeployPrivateKey is the private half of the deploy keypair. It is
	// authentication/transport only: it lets inforge SSH the host (which trusts
	// the public half via provision.sh) to realize host-level resources such as
	// tls-termination. It encrypts nothing.
	//
	// It is required on a preview as well as a deploy. A preview opens no SSH
	// connection, but the key is an INPUT to every remote command resource, so a
	// keyless run diffs an empty key against the one in state and previews a
	// spurious update for every host command. See program.resolveDeployKey.
	//
	// It is a deploy-time secret: never read from variables.yaml (hence
	// `yaml:"-"`), but injected by the program from stack config
	// (deploy_private_key) or INFORGE_DEPLOY_PRIVATE_KEY. A committed private key
	// would violate "never commit secrets".
	DeployPrivateKey string `yaml:"-"`
}

// EnvironmentVariables is the parsed variables.yaml for one environment. Which
// regions deploy and all provider config (credentials + per-region
// realizations) now live in regions.yaml (see internal/regions); variables.yaml
// carries only the base domain and SSH material.
type EnvironmentVariables struct {
	BaseDomain    string              `yaml:"base_domain"`
	SSH           SSHConfig           `yaml:"ssh"`
	Observability ObservabilityConfig `yaml:"observability"`
	Security      SecurityConfig      `yaml:"security"`
}

// SecurityConfig is the optional env-level edge security tier (ADR-0043): a blanket IP
// rate limit and (slice 2) the CrowdSec agent, applied to every public edge (ingress +
// gateway) host. Both are off unless enabled. Like ObservabilityConfig it lives in
// variables.yaml and is struct-decoded — there is no JSON schema for the block. An edge
// opts the whole tier out with `security: false` on its ingress/gateway spec.
type SecurityConfig struct {
	Crowdsec  CrowdsecConfig  `yaml:"crowdsec"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

// CrowdsecConfig toggles the per-edge-host CrowdSec agent + firewall bouncer (slice 2).
// Version pins the installed release (else the package DefaultVersion); Console opts in
// to dashboard enrollment (gated on a reserved secret). The fields are carried now so
// the authoring surface is stable; the realization lands in the CrowdSec slice.
type CrowdsecConfig struct {
	Enabled bool   `yaml:"enabled"`
	Version string `yaml:"version,omitempty"`
	Console bool   `yaml:"console,omitempty"`
}

// RateLimitConfig is the blanket IP rate limit (ADR-0043): one uniform limit applied to
// every public server on an edge — a security floor, not per-route tuning (per-route /
// per-identity limits are a gateway concern, ADR-0044). Keying is always the client IP.
type RateLimitConfig struct {
	Enabled           bool `yaml:"enabled"`
	RequestsPerSecond int  `yaml:"requests_per_second"`
	Burst             int  `yaml:"burst"`
	MaxConnections    int  `yaml:"max_connections"`
}

// RateLimitZoneStem is the fixed nginx zone stem for the edge-uniform limit. It is a
// constant (not per-route) because one limit covers the whole edge (ADR-0043).
const RateLimitZoneStem = "edge"

// ResolvedRateLimit returns the profile to stamp on an edge's public servers, or nil
// when rate limiting is disabled or has nothing to enforce (both limits zero). The
// program applies it uniformly to every server of every edge that has not opted out.
func (s SecurityConfig) ResolvedRateLimit() *RateLimitProfile {
	rl := s.RateLimit
	if !rl.Enabled || (rl.RequestsPerSecond <= 0 && rl.MaxConnections <= 0) {
		return nil
	}
	return &RateLimitProfile{
		Name:    RateLimitZoneStem,
		RPS:     rl.RequestsPerSecond,
		Burst:   rl.Burst,
		MaxConn: rl.MaxConnections,
	}
}

// ObservabilityConfig is the optional env-level observability block (ADR-0031,
// ADR-0038). When OTLPEndpoint is set (and the matching auth secret exists in the
// env's secrets.enc.yaml), inforge installs the host VM-metrics collector on every
// VM. When it is empty, no collector is installed — the agent is always-on but gated
// on this config being present, so an env that has not set up Grafana Cloud gets
// nothing. OTLPEndpoint is the non-secret OTLP/HTTP base URL; the Basic-auth
// credential is NOT here — it lives in secrets.enc.yaml under the reserved
// observability container (see otelcol.AuthSecretRef).
//
// GrafanaURL is the non-secret base URL of the Grafana instance inforge pushes
// dashboards + alerts to (ADR-0038). When set (and the reserved grafana_token secret
// exists), `inforge deploy` realizes the built-in dashboards for this env; empty
// means no dashboards are managed. The service-account token is NOT here — it is the
// reserved secret observability/grafana_token (see grafana.TokenKey).
type ObservabilityConfig struct {
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	GrafanaURL   string `yaml:"grafana_url"`

	// BuiltInDashboards / BuiltInAlerts opt this env in or out of the generated
	// dashboards (ADR-0038 slice 2) and alert rules (slice 3). Both default to true
	// (a *bool distinguishes "unset" from an explicit false); use the accessors.
	BuiltInDashboards *bool `yaml:"built_in_dashboards"`
	BuiltInAlerts     *bool `yaml:"built_in_alerts"`

	// DefaultProfile is the notification profile (defined in observability/
	// notifications.yaml) that built-in alerts and any alert omitting `profile:`
	// route through. Required once alerts are managed (built-in or custom).
	DefaultProfile string `yaml:"default_profile"`
}

// DashboardsEnabled reports whether this env's built-in dashboards are managed
// (default true; opt out with `built_in_dashboards: false`).
func (o ObservabilityConfig) DashboardsEnabled() bool {
	return o.BuiltInDashboards == nil || *o.BuiltInDashboards
}

// AlertsEnabled reports whether this env's built-in alert rules are managed
// (default true; opt out with `built_in_alerts: false`).
func (o ObservabilityConfig) AlertsEnabled() bool {
	return o.BuiltInAlerts == nil || *o.BuiltInAlerts
}

// NotificationsSpec is the parsed observability/notifications.yaml (ADR-0038
// slice 3): the env's reusable contact points and the profiles (per-team routing
// tables) that map an alert's severity to one of them.
type NotificationsSpec struct {
	ContactPoints map[string]ContactPoint `yaml:"contact_points"`
	Profiles      map[string]Profile      `yaml:"profiles"`
}

// ContactPoint is one named notification destination. Exactly one integration is
// set. Secret-bearing integrations (OnCall) reference a reserved-secret key under
// the observability namespace rather than inlining the secret.
type ContactPoint struct {
	OnCall  *OnCallIntegration  `yaml:"oncall,omitempty"`
	Email   *EmailIntegration   `yaml:"email,omitempty"`
	Webhook *WebhookIntegration `yaml:"webhook,omitempty"`
}

// OnCallIntegration routes to a Grafana IRM / OnCall integration — the idiomatic
// path when the alert rules are Grafana-managed and IRM lives in the same stack
// (native OnCall payloads: alert grouping + auto-resolve, unlike a raw webhook).
// URLSecret names the reserved-secret key (observability/<URLSecret>) holding the
// integration's inbound URL, which is a capability credential and so is never
// inlined in the manifest. Escalation chains and on-call schedules are managed in
// Grafana IRM; inforge only references the integration URL.
type OnCallIntegration struct {
	URLSecret string `yaml:"url_secret"`
}

// EmailIntegration routes to one or more email addresses.
type EmailIntegration struct {
	Addresses []string `yaml:"addresses"`
}

// WebhookIntegration POSTs to a URL (e.g. a Slack incoming webhook).
type WebhookIntegration struct {
	URL string `yaml:"url"`
}

// Profile is a routing table: severity (critical|warning|info) -> where its alerts
// go. Every severity an alert can resolve to must be present.
type Profile map[string]ProfileRoute

// ProfileRoute is one severity's destination: exactly one of ContactPoint (a name
// in the same file's contact_points) or Muted (suppress — inforge omits the alert).
type ProfileRoute struct {
	ContactPoint string `yaml:"contact_point,omitempty"`
	Muted        bool   `yaml:"muted,omitempty"`
}

// AlertsSpec is the parsed observability/alerts.yaml (ADR-0038 slice 3): the env's
// custom alert rules, alongside the generated built-ins.
type AlertsSpec struct {
	Alerts []AlertSpec `yaml:"alerts"`
}

// AlertSpec is the simplified alert authoring shape (ADR-0038). expr is any PromQL
// returning an instant/range vector; condition is a threshold ("> 90", "< 0.01",
// operators > < >= <=) applied to each series' reduced value. Profile empty ⇒ the
// env's default_profile. Summary/Labels flow to the notification (Summary may use
// {{ $labels.x }} / {{ $value }}).
type AlertSpec struct {
	Name      string            `yaml:"name"`
	Expr      string            `yaml:"expr"`
	Condition string            `yaml:"condition"`
	For       string            `yaml:"for,omitempty"`
	Severity  string            `yaml:"severity"`
	Profile   string            `yaml:"profile,omitempty"`
	Summary   string            `yaml:"summary,omitempty"`
	Labels    map[string]string `yaml:"labels,omitempty"`
}

// Resources is the full set of resource specs for one region.
type Resources struct {
	Network         []NetworkSpec
	Compute         []ComputeSpec
	DatabaseCluster []DatabaseClusterSpec
	Database        []DatabaseSpec
	Service         []ServiceSpec
	PKI             []PKIResourceSpec
	Ingress         []IngressSpec
	App             []AppSpec
	Gateway         []GatewaySpec
}

// HasAny reports whether the set declares any resource of any kind. Used to decide
// whether a scope (notably the optional global slice) is worth realizing at all —
// an absent global/ dir loads as the zero Resources. Counting every field keeps it
// honest as new resource kinds are added.
func (r Resources) HasAny() bool {
	return len(r.Network)+len(r.Compute)+len(r.DatabaseCluster)+len(r.Database)+
		len(r.Service)+len(r.PKI)+len(r.Ingress)+len(r.App)+len(r.Gateway) > 0
}

// ProviderDefaults are project-level provider fallbacks. When a resource spec omits
// its provider field, the effective provider is resolved from this block.
// Compute applies to both network and compute specs (they share one cloud provider).
// Database maps engine name to provider. When no override and no default is
// configured, a database engine resolves to "self-hosted" (ADR-0036) — inforge
// installs and runs the engine on a compute host it provisions.
type ProviderDefaults struct {
	Compute  string            `yaml:"compute"`
	Database map[string]string `yaml:"database"`
}

// SelfHostedProvider is the database provider value meaning inforge runs the engine
// itself on a compute host (ADR-0036), as opposed to a managed cloud database. It is
// the default when a database-cluster names no provider and none is configured.
const SelfHostedProvider = "self-hosted"

// ResolveProvider returns the effective provider for a resource: override wins if
// non-empty, then the class default (engine-keyed for databases). Returns "" when
// no provider is configured.
func ResolveProvider(override, class, engine string, d ProviderDefaults) string {
	if override != "" {
		return override
	}
	switch class {
	case "compute", "network":
		return d.Compute
	case "database":
		if p, ok := d.Database[engine]; ok && p != "" {
			return p
		}
		// No override and no configured default: inforge self-hosts the engine
		// (ADR-0036). This is the flipped default that retires managed-by-default.
		return SelfHostedProvider
	}
	return ""
}
