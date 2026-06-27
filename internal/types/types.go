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

// DatabaseSpec is one managed database resource.
type DatabaseSpec struct {
	Name      string `yaml:"name"`
	Container string `yaml:"container"`
	Provider  string `yaml:"provider"`
	Engine    string `yaml:"engine"` // "postgresql"
	Branch    string `yaml:"branch"` // default "main"
	Database  string `yaml:"database"`
	Owner     string `yaml:"owner"` // PostgreSQL role that owns the database
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
	Reload           string            `yaml:"reload,omitempty"`             // optional ExecReload command to apply a renewed mesh leaf without downtime (e.g. "/bin/kill -HUP $MAINPID"); absent -> renewal restarts the unit
	Ingress          string            `yaml:"ingress,omitempty"`            // FK -> ingress resource name (same scope) whose nginx fronts this service's Routes; required when Routes is non-empty
	Routes           []RouteSpec       `yaml:"routes,omitempty"`             // typed inbound routes (tls-termination / forward) realized on the referenced ingress's nginx
	HealthProbesPort int               `yaml:"health_probes_port,omitempty"` // backend port this service serves health checks on; surfaced through the ingress's public health port, Host-demuxed by the service FQDN (requires Ingress)
	ExposedPorts     []ExposedPort     `yaml:"exposed_ports,omitempty"`      // ports the service binds that inforge opens on the host's private network only (never the public internet), for peer/service-to-service traffic; needs no ingress (ADR-0029)
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
// independent root each); global/pki/<name> is region-less. The age-encrypted
// pki.enc.yaml sidecar (root key + cert) and the generate command land with the
// PKI resource Grant behavior (slice C of #117); this slice defines and validates
// the declarative shape only.
type PKIResourceSpec struct {
	Name      string `yaml:"name"`
	Container string `yaml:"container"`
	Topology  string `yaml:"topology"`           // "root-only" — the only valid topology for a PKI resource
	Validity  string `yaml:"validity,omitempty"` // optional CA validity (e.g. "10y"); enforced when generation lands (slice C)
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
	FQDN    string // canonical service FQDN matched as server_name / Host
	Target  int    // backend port the service serves health checks on
	Backend string // backend address nginx proxies to ("127.0.0.1" co-located; private IP cross-host)
}

// NetworkOutputs are the values a NetworkProvider returns after creating a
// network, consumed by the compute provider.
type NetworkOutputs struct {
	NetworkID pulumi.StringOutput
	SubnetID  pulumi.StringOutput
}

// ComputeOutputs are the values a ComputeProvider returns after creating a host.
// PrivateIP is the host's address on its attached private network — empty in
// preview, and used by the ingress tier to proxy_pass to a backend that lives on
// a different host within the same Hetzner Network (cross-host routing).
type ComputeOutputs struct {
	PublicIP  pulumi.StringOutput
	PrivateIP pulumi.StringOutput
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
	// Encrypted maps container -> KEY -> plaintext for `vault:<KEY>` secrets,
	// decrypted once per deploy from resources/<env>/secrets.enc.yaml
	// (ADR-0017). Region-independent — the store is env-scoped. Nil when the
	// environment declares no vault: sources.
	Encrypted map[string]map[string]string
}

// ResolveScoped looks up name in a region-keyed output map (Compute/Database),
// honoring the one allowed cross-region form: a "global/" name prefix redirects
// the lookup to the region-less "global" slot, independent of the consuming
// service's region. It returns the value, the resolved (region, bareName) for
// error messages, and whether it was found. This is the single source of the
// global/ redirect rule — both the Source DSL (ref:) and grant target resolution
// MUST use it so they resolve a global resource identically (ADR-0025).
func ResolveScoped[V any](m map[string]map[string]V, region, name string) (value V, resolvedRegion, bareName string, found bool) {
	resolvedRegion, bareName = region, name
	if rest, ok := strings.CutPrefix(name, "global/"); ok {
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
// concern: secrets are fetched at runtime by inforge-bootstrap, so there is no
// bootstrap document.
type ComputeProvider interface {
	Create(ctx *pulumi.Context, spec ComputeSpec, network NetworkOutputs, env, abstractRegion, domain, manifest string, fw FirewallPorts) (ComputeOutputs, error)
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
type IngressProvider interface {
	Realize(ctx *pulumi.Context, hostKey string, host ComputeOutputs, deployUser string, routes []IngressRoute, apps []IngressApp, health []IngressHealth, healthPort int, backendIPs map[string]pulumi.StringOutput, env string, dependsOn []pulumi.Resource) error
}

// DatabaseProvider creates a managed database.
type DatabaseProvider interface {
	Create(ctx *pulumi.Context, spec DatabaseSpec, env, region string) (DatabaseOutputs, error)
}

// ServiceSecretsBundle is everything inforge needs to deliver one service's
// runtime secrets contract to its host: the provider coordinates and env-var ->
// vault-key mapping for the descriptor, plus the per-service machine identity's
// universal-auth credentials (Outputs, ClientSecret sensitive) the bootstrapper
// logs in with. Project is the workspace ID (what the on-host fetcher sends as
// the workspaceId query param), so it is an Output resolved at deploy time. The
// program age-encrypts {ClientId, ClientSecret} to the host key and writes the
// descriptor + credential; the provider that produced the bundle stays unaware
// of hosts and SSH.
type ServiceSecretsBundle struct {
	Project      pulumi.StringOutput // workspace ID
	ClientID     pulumi.StringOutput // identity universal-auth client ID
	ClientSecret pulumi.StringOutput // identity universal-auth client secret (sensitive)
	ProviderKind string              // e.g. "infisical"
	URL          string              // provider site URL
	Environment  string              // provider environment slug
	SecretPath   string              // the service's scoped path, e.g. "/ghost"
	// Env maps each service env var name to its vault key relative to SecretPath
	// (e.g. "DATABASE_URL" -> "infra/DATABASE_URL").
	Env map[string]string
}

// ServiceSecretsProvisioner provisions one service's runtime secrets: it writes
// the service's infra secrets under its scoped path and mints a per-service
// machine identity scoped read-only to that path, returning the bundle the
// program needs to write the descriptor + host-key-encrypted credential. It
// returns a nil bundle (no error) when the service has no secrets to deliver.
type ServiceSecretsProvisioner interface {
	// grantSecrets are env-var → value-field secret outputs already resolved by the
	// program from the service's database grants (ADR-0025); the provisioner writes
	// them into the same infra batch alongside the environment.yaml-derived secrets.
	ProvisionService(ctx *pulumi.Context, svc ServiceSpec, env, region string, all AllOutputs, grantSecrets map[string]pulumi.StringOutput) (*ServiceSecretsBundle, error)
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
	// tls-termination. It encrypts nothing. Empty outside deploy (e.g. preview),
	// where no remote command runs.
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
	BaseDomain string    `yaml:"base_domain"`
	SSH        SSHConfig `yaml:"ssh"`
}

// Resources is the full set of resource specs for one region.
type Resources struct {
	Network  []NetworkSpec
	Compute  []ComputeSpec
	Database []DatabaseSpec
	Service  []ServiceSpec
	PKI      []PKIResourceSpec
	Ingress  []IngressSpec
	App      []AppSpec
}

// HasAny reports whether the set declares any resource of any kind. Used to decide
// whether a scope (notably the optional global slice) is worth realizing at all —
// an absent global/ dir loads as the zero Resources. Counting every field keeps it
// honest as new resource kinds are added.
func (r Resources) HasAny() bool {
	return len(r.Network)+len(r.Compute)+len(r.Database)+
		len(r.Service)+len(r.PKI)+len(r.Ingress)+len(r.App) > 0
}

// ProviderDefaults are project-level provider fallbacks. When a resource spec omits
// its provider field, the effective provider is resolved from this block.
// Compute applies to both network and compute specs (they share one cloud provider).
// Database maps engine name to provider (e.g. "postgresql": "neon").
type ProviderDefaults struct {
	Compute  string            `yaml:"compute"`
	Database map[string]string `yaml:"database"`
}

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
		return ""
	}
	return ""
}
