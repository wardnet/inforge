// Package types defines the declarative resource specs, provider interfaces,
// and the manifest contract that make up the inforge Toolkit domain.
//
// Specs are unmarshalled from a project's YAML resource files (see
// internal/loader); defaults that yaml.v3 cannot apply are normalised by the
// loader after unmarshal. Provider implementations live in the per-provider
// plugin packages and satisfy the interfaces declared here.
package types

import "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

// NetworkSpec is one network resource. A network is either public (no parent)
// or private (carved out of a parent network's CIDR).
type NetworkSpec struct {
	Name       string `yaml:"name"`
	Instance   int    `yaml:"instance"`
	Container  string `yaml:"container"`
	Provider   string `yaml:"provider"`
	Type       string `yaml:"type"` // "public" (default) | "private"
	CIDR       string `yaml:"cidr"`
	SubnetCIDR string `yaml:"subnet_cidr,omitempty"`
	Parent     string `yaml:"parent,omitempty"`
}

// ComputeSpec is one compute resource — a host runtime. Its cpus/memory are
// resolved from the size table (see internal/sizes), not declared here.
type ComputeSpec struct {
	Name          string `yaml:"name"`
	Kind          string `yaml:"kind"` // "vm" (default; only supported kind) | "cluster" (reserved)
	Container     string `yaml:"container"`
	Provider      string `yaml:"provider"`
	Network       string `yaml:"network"` // FK -> network specKey
	Size          string `yaml:"size"`    // resolved against the size table
	Image         string `yaml:"image"`
	CloudInit     string `yaml:"cloud_init,omitempty"` // path relative to the compute dir
	InstanceCount int    `yaml:"instance_count"`       // default 1; expands into specKeys name-01..name-NN
}

// DnsSpec is one DNS record.
type DnsSpec struct {
	Name      string `yaml:"name"`
	Instance  int    `yaml:"instance"`
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

// ServiceSpec is one service resource — a workload hosted on a compute.
type ServiceSpec struct {
	Name      string `yaml:"name"`
	Container string `yaml:"container"`
	Provider  string `yaml:"provider"`
	Host      string `yaml:"host"` // FK -> an expanded compute specKey whose kind=vm
	Type      string `yaml:"type"` // "raw" (built) | "container" (reserved)
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

// NetworkProvider creates a network for one spec in one region.
type NetworkProvider interface {
	Create(ctx *pulumi.Context, spec NetworkSpec, env, abstractRegion string) (NetworkOutputs, error)
}

// ComputeProvider creates one compute instance, wiring in its network, the
// host domain, the assembled manifest, and the bootstrap document (empty when
// the manifest has no secrets).
type ComputeProvider interface {
	Create(ctx *pulumi.Context, spec ComputeSpec, network NetworkOutputs, env, abstractRegion, domain, manifest, bootstrapDoc string) (ComputeOutputs, error)
}

// DnsProvider creates a DNS record pointing at a compute instance.
type DnsProvider interface {
	Create(ctx *pulumi.Context, spec DnsSpec, compute ComputeOutputs, recordName string) error
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

// ManifestContribution is a set of fields a contributor adds to a service's
// manifest. Individual values may be marked secret via manifest.Secret.
type ManifestContribution = map[string]any

// ComputeInstanceManifestContributor adds fields to a compute's manifest. The
// trigger for VM bootstrap is the presence of secret values in the assembled
// manifest, so this contract carries no explicit bootstrap argument.
type ComputeInstanceManifestContributor interface {
	ContributeToManifest(spec ComputeSpec, resources Resources, env, region string) (ManifestContribution, error)
}

// SSHConfig holds the per-environment SSH material applied to hosts.
type SSHConfig struct {
	AuthorizedKeys  string `yaml:"authorizedKeys"`
	DeployPublicKey string `yaml:"deployPublicKey"`
}

// RegionEntry is one entry in an environment's regions[] — an abstract region
// this environment deploys into, with its per-region provider overrides.
type RegionEntry struct {
	Name      string                    `yaml:"name"`
	Providers map[string]map[string]any `yaml:"providers"`
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
	Network  []NetworkSpec
	Compute  []ComputeSpec
	DNS      []DnsSpec
	Database []DatabaseSpec
	Secrets  []SecretsSpec
	Service  []ServiceSpec
}
