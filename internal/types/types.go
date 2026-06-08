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

// FirewallSpec declares the inbound firewall rules for a compute resource.
// SSH (22) is always permitted regardless of what is declared here.
// If absent, inforge applies its built-in defaults (22, 80, 443, 853).
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
	CloudInit     string          `yaml:"cloud_init,omitempty"` // path relative to the compute dir
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

// SecretsEntry is one entry in a secrets resource, naming where the value comes
// from via the source DSL (see internal/validate for the grammar).
type SecretsEntry struct {
	Source string `yaml:"source"`
}

// SecretsSpec is one secrets resource: a named set of secrets to materialise.
type SecretsSpec struct {
	Name      string                  `yaml:"name"`
	Container string                  `yaml:"container"`
	Provider  string                  `yaml:"provider"`
	Secrets   map[string]SecretsEntry `yaml:"secrets"`
}

// DeployUserSpec configures the deploy user provisioned on a compute instance
// at VM-init time. The SSH key material comes from SSHConfig.DeployPublicKey.
type DeployUserSpec struct {
	Name string `yaml:"name"`
}

// IngressSpec is one inbound routing entry a service exposes through its host's
// tls-termination resource. A service carries a list of these (ServiceSpec.Ingress):
// at most one catch-all and at most one non-catch-all (terminate OR named
// passthrough), since the non-catch-all entry owns the service's auto-derived
// "<svc>.svc" FQDN. A terminate entry's SNI/ACME-cert FQDNs are the auto-derived
// service FQDN plus any Vanity entries; a catch-all has no FQDN (it matches every
// unmatched SNI). The env-scoped FQDNs are derived at realization time (see
// naming.ServiceFQDN / naming.ExpandVanity), not authored here.
type IngressSpec struct {
	Port     int    `yaml:"port"`               // local port traffic is forwarded to
	TLS      string `yaml:"tls,omitempty"`      // "terminate" (default) | "passthrough"
	Catchall bool   `yaml:"catchall,omitempty"` // at most one per host; matches all unmatched SNIs; implies passthrough
	// Vanity adds extra public FQDNs (beyond the auto-derived "<svc>.svc" name) a
	// terminate/named route serves: a bare token is env+region-scoped, anything
	// with a dot or a {BASE_DOMAIN}/{ENV}/{REGION_SLUG} placeholder is a literal
	// FQDN. Ignored on a catch-all (it has no SNI).
	Vanity []string `yaml:"vanity,omitempty"`
	// ProxyProtocol enables the PROXY protocol to the upstream on passthrough/catchall
	// routes so the backend learns the real client address: "" (off) | "v1" | "v2".
	ProxyProtocol string `yaml:"proxy_protocol,omitempty"`
}

// Ingress TLS modes and the default applied when IngressSpec.TLS is empty.
const (
	IngressTLSTerminate   = "terminate"
	IngressTLSPassthrough = "passthrough"
)

// Mode returns the effective TLS mode for an ingress: passthrough when the
// ingress is a catch-all (terminating arbitrary SNIs is not supported) or when
// explicitly set, otherwise the default "terminate".
func (i IngressSpec) Mode() string {
	if i.Catchall {
		return IngressTLSPassthrough
	}
	if i.TLS == "" {
		return IngressTLSTerminate
	}
	return i.TLS
}

// ServiceSpec is one service resource — a workload hosted on a compute.
type ServiceSpec struct {
	Name      string       `yaml:"name"`
	Container string       `yaml:"container"`
	Provider  string       `yaml:"provider"`
	Host      string       `yaml:"host"`              // FK -> an expanded compute specKey whose kind=vm
	Type      string       `yaml:"type"`              // "raw" (built) | "container" (reserved)
	User      string        `yaml:"user,omitempty"`    // no-login system user the service runs as; raw only
	Ingress   []IngressSpec `yaml:"ingress,omitempty"` // inbound routes via the host's tls-termination (≤1 catch-all, ≤1 non-catch-all)
}

// TLSTerminationSpec declares a host-level TLS terminator — a capability the
// compute provider realizes on a host to terminate inbound TLS and reverse-proxy
// to the services running there. On Hetzner this is realized by Caddy (ACME /
// Let's Encrypt); another provider could realize the same resource with a
// managed load balancer + ACM. Per-service ingress (ServiceSpec.Ingress) feeds
// this terminator, which writes one vhost per service.
type TLSTerminationSpec struct {
	Name      string `yaml:"name"`
	Container string `yaml:"container"`
	Provider  string `yaml:"provider"`
	Compute   string `yaml:"compute"` // FK -> an expanded compute specKey whose kind=vm
}

// TLSRoute is one inbound routing entry a TLS terminator realizes on its host,
// derived from one ingress-bearing service. It is provider-agnostic: the Hetzner
// provider translates it to Caddy, but another provider could realize the same
// route with a managed load balancer. The FQDN is fully resolved (env + region
// slug + base domain) before it reaches the provider, so the provider stays a
// pure renderer/installer and never re-derives names.
//
// Mode selects whether the terminator decrypts the connection:
//   - "terminate": ACME TLS is terminated and traffic reverse-proxied to Port.
//   - "passthrough": the raw TLS stream is forwarded by SNI to Port; the backend
//     owns its own TLS. ProxyProtocol optionally prepends a PROXY header so the
//     backend learns the real client address.
//
// Catchall marks the single per-host route that matches every SNI not matched by
// a named route (always passthrough). Its FQDN is unused for matching.
type TLSRoute struct {
	Service       string
	FQDN          string // fully-qualified, env-scoped SNI matched (empty/unused when Catchall)
	Port          int    // local port traffic is forwarded to
	Mode          string // IngressTLSTerminate | IngressTLSPassthrough
	Catchall      bool
	ProxyProtocol string // "", "v1", "v2"
}

// NetworkOutputs are the values a NetworkProvider returns after creating a
// network, consumed by the compute provider.
type NetworkOutputs struct {
	NetworkID pulumi.StringOutput
	SubnetID  pulumi.StringOutput
}

// ComputeOutputs are the values a ComputeProvider returns after creating a host.
type ComputeOutputs struct {
	PublicIP pulumi.StringOutput
}

// DatabaseOutputs are the values a DatabaseProvider returns after creating a database.
type DatabaseOutputs struct {
	ConnectionURL pulumi.StringOutput
}

// AllOutputs collects per-region outputs so the secrets backend can resolve
// cross-resource references. Keyed by region, then specKey/name.
type AllOutputs struct {
	Compute  map[string]map[string]ComputeOutputs
	Database map[string]map[string]DatabaseOutputs
}

// NetworkProvider creates a network for one spec in one region. Returns a map
// from subnet name to its outputs so callers can look up a specific subnet.
type NetworkProvider interface {
	Create(ctx *pulumi.Context, spec NetworkSpec, env, abstractRegion string) (map[string]NetworkOutputs, error)
}

// ComputeProvider creates one compute instance, wiring in its network, the host
// domain, and the assembled (plain, secret-free) manifest. Secret delivery is no
// longer a compute-creation concern: secrets are fetched at runtime by
// inforge-bootstrap, so there is no bootstrap document.
type ComputeProvider interface {
	Create(ctx *pulumi.Context, spec ComputeSpec, network NetworkOutputs, env, abstractRegion, domain, manifest string) (ComputeOutputs, error)
}

// DnsProvider creates a derived DNS record pointing at a compute instance, on a
// region's DNS authority.
type DnsProvider interface {
	CreateRecord(ctx *pulumi.Context, rec DnsRecord, target ComputeOutputs) error
}

// TLSTerminationProvider realizes a tls-termination spec on its host. host
// carries the target's public IP; deployUser is the sudo-capable account
// inforge connects as over SSH (the host's deploy user); routes are the inbound
// routing entries (terminate / passthrough / catch-all), with FQDNs already
// env-scoped by the caller. Realize installs the terminator once per host, writes
// its config, and reloads — and must be safe to re-run as services are added.
//
// The signature is grounded in the Hetzner/Caddy consumer: the provider is a
// pure installer over SSH, so it needs the host, the connection identity, and
// the resolved routes — nothing more. env scopes the names of the Pulumi
// resources it creates. dependsOn carries the host's cloud-init readiness gate
// (and any other prerequisites): the provider must make its first per-host SSH
// command depend on it so realization never races the host's deploy_user
// creation.
type TLSTerminationProvider interface {
	Realize(ctx *pulumi.Context, spec TLSTerminationSpec, host ComputeOutputs, deployUser string, routes []TLSRoute, env string, dependsOn []pulumi.Resource) error
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
	ProvisionService(ctx *pulumi.Context, svc ServiceSpec, res Resources, env, region string, all AllOutputs) (*ServiceSecretsBundle, error)
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
	Network        []NetworkSpec
	Compute        []ComputeSpec
	Database       []DatabaseSpec
	Secrets        []SecretsSpec
	Service        []ServiceSpec
	TLSTermination []TLSTerminationSpec
}
