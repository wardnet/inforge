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

// DnsSpec is one DNS record.
type DnsSpec struct {
	Name      string `yaml:"name"`
	Container string `yaml:"container"`
	Provider  string `yaml:"provider"`
	Compute   string `yaml:"compute"` // FK -> an expanded compute specKey
	Subdomain string `yaml:"subdomain"`
	Proxied   bool   `yaml:"proxied"`
}

// DatabaseSpec is one managed database resource.
type DatabaseSpec struct {
	Name      string `yaml:"name"`
	Container string `yaml:"container"`
	Provider  string `yaml:"provider"`
	Engine    string `yaml:"engine"` // "postgresql"
	Branch    string `yaml:"branch"` // default "main"
	Database  string `yaml:"database"`
	Role      string `yaml:"role"`
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

// IngressSpec exposes a service for inbound traffic through its host's
// tls-termination resource. Declaring ingress means "terminate TLS for this
// host and reverse-proxy to its local port": the terminator writes one
// per-service vhost that does exactly that. Non-TLS exposure is a firewall
// concern, not a terminator one, so there is no opt-out — ingress always
// implies ACME TLS. Hostname is a host label (like DnsSpec.Subdomain); the
// env-scoped FQDN it resolves to is derived at realization time, not authored
// here.
type IngressSpec struct {
	Hostname string `yaml:"hostname"` // host label, env-scoped into an FQDN at realization
	Port     int    `yaml:"port"`     // local port the service listens on
}

// ServiceSpec is one service resource — a workload hosted on a compute.
type ServiceSpec struct {
	Name      string       `yaml:"name"`
	Container string       `yaml:"container"`
	Provider  string       `yaml:"provider"`
	Host      string       `yaml:"host"`              // FK -> an expanded compute specKey whose kind=vm
	Type      string       `yaml:"type"`              // "raw" (built) | "container" (reserved)
	User      string       `yaml:"user,omitempty"`    // no-login system user the service runs as; raw only
	Ingress   *IngressSpec `yaml:"ingress,omitempty"` // optional inbound exposure via the host's tls-termination
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

// Vhost is one per-service reverse-proxy entry a TLS terminator realizes on its
// host: an already-env-scoped FQDN that terminates TLS (ACME) and proxies to a
// local port. The program derives these from each ingress-bearing service whose
// host the terminator covers; the FQDN is fully resolved (env + region slug +
// base domain) before it reaches the provider, so the provider stays a pure
// renderer/installer and never re-derives names.
type Vhost struct {
	Service string // service name; the terminator writes one vhost file per service
	FQDN    string // fully-qualified, env-scoped host name the terminator serves
	Port    int    // local port on the host the service listens on
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

// DnsProvider creates a DNS record pointing at a compute instance.
type DnsProvider interface {
	Create(ctx *pulumi.Context, spec DnsSpec, compute ComputeOutputs) error
}

// TLSTerminationProvider realizes a tls-termination spec on its host. host
// carries the target's public IP; deployUser is the sudo-capable account
// inforge connects as over SSH (the host's deploy user); vhosts are the
// per-service reverse-proxy entries, with FQDNs already env-scoped by the
// caller. Realize installs the terminator once per host, writes one vhost per
// service, and reloads — and must be safe to re-run as services are added.
//
// The signature is grounded in the Hetzner/Caddy consumer: the provider is a
// pure installer over SSH, so it needs the host, the connection identity, and
// the resolved vhosts — nothing more. env scopes the names of the Pulumi
// resources it creates. dependsOn carries the host's cloud-init readiness gate
// (and any other prerequisites): the provider must make its first per-host SSH
// command depend on it so realization never races the host's deploy_user
// creation.
type TLSTerminationProvider interface {
	Realize(ctx *pulumi.Context, spec TLSTerminationSpec, host ComputeOutputs, deployUser string, vhosts []Vhost, env string, dependsOn []pulumi.Resource) error
}

// DatabaseProvider creates a managed database.
type DatabaseProvider interface {
	Create(ctx *pulumi.Context, spec DatabaseSpec, env, region string) (DatabaseOutputs, error)
}

// SecretsBackendProvider materialises a secrets resource into a backend,
// resolving references against the outputs produced so far.
type SecretsBackendProvider interface {
	Create(ctx *pulumi.Context, spec SecretsSpec, env, region string, all AllOutputs) error
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
	// (deploy_private_key) or INFORGE_DEPLOY_PRIVATE_KEY — the same pattern as
	// the OIDC token. A committed private key would violate "never commit
	// secrets".
	DeployPrivateKey string `yaml:"-"`
}

// RegionEntry is one entry in an environment's regions[] — an abstract region
// this environment deploys into. It is a plain selector; the provider-specific
// realization of each region lives in the provider config under
// providers.<name>.regions.
type RegionEntry struct {
	Name string `yaml:"name"`
}

// EnvironmentVariables is the parsed variables.yaml for one environment.
type EnvironmentVariables struct {
	BaseDomain string                    `yaml:"base_domain"`
	Regions    []RegionEntry             `yaml:"regions"`
	Providers  map[string]map[string]any `yaml:"providers"`
	SSH        SSHConfig                 `yaml:"ssh"`
}

// Resources is the full set of resource specs for one region.
type Resources struct {
	Network        []NetworkSpec
	Compute        []ComputeSpec
	DNS            []DnsSpec
	Database       []DatabaseSpec
	Secrets        []SecretsSpec
	Service        []ServiceSpec
	TLSTermination []TLSTerminationSpec
}
