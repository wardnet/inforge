package meshplan

import (
	"fmt"
	"sort"

	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
)

// defaultSSHUser mirrors the service deploy descriptor's fallback: hosts with
// no declared deploy_user are provisioned with "deploy".
const defaultSSHUser = "deploy"

// DeployTarget is one mesh host in the mesh deploy descriptor: enough for the
// deploy CLI to SSH the host and trigger its material pull
// (`systemctl start wardnet-mesh-renew.service`) after the post-up baseline
// mint — the only push in the pull-based delivery (ADR-0033), and it pushes a
// signal, never material.
type DeployTarget struct {
	// Host is the canonical compute key (e.g. "bridge-01") — the same key the
	// per-host provider path (/<hostKey>) and ServicesByHost grouping use.
	Host    string `yaml:"host" json:"host"`
	HostDNS string `yaml:"host_dns" json:"host_dns"`
	SSHUser string `yaml:"ssh_user" json:"ssh_user"`
	// Scope is the host's mesh scope: its region name, or pki.ScopeGlobal.
	Scope string `yaml:"scope" json:"scope"`
}

// DeployDescriptor is the `meshDeployDescriptor` stack output (the mesh sibling
// of deployDescriptor / appDeployDescriptor).
type DeployDescriptor struct {
	Environment string         `yaml:"environment" json:"environment"`
	Targets     []DeployTarget `yaml:"targets" json:"targets"`
}

// BuildDeployDescriptor derives the mesh deploy descriptor: one target per mesh
// host (a host running ≥1 pki: service), across every region plus the global
// scope. Derived purely from resolved resources — no cloud outputs — like its
// service/app siblings.
func BuildDeployDescriptor(env, baseDomain string, regional, global types.Resources, table regions.Table) (DeployDescriptor, error) {
	desc := DeployDescriptor{Environment: env}

	regionNames := make([]string, 0, len(table))
	for region := range table {
		regionNames = append(regionNames, region)
	}
	sort.Strings(regionNames)

	appendScope := func(res types.Resources, scope, slug string) {
		canonical := naming.CanonicalComputeKeys(res.Compute)
		deployUsers := naming.DeployUsersByHost(res.Compute)
		byHost := ServicesByHost(res, canonical)
		for _, hostKey := range HostKeys(byHost) {
			sshUser := deployUsers[hostKey]
			if sshUser == "" {
				sshUser = defaultSSHUser
			}
			desc.Targets = append(desc.Targets, DeployTarget{
				Host:    hostKey,
				HostDNS: naming.HostFQDN(env, slug, naming.BareComputeName(hostKey), baseDomain),
				SSHUser: sshUser,
				Scope:   scope,
			})
		}
	}

	for _, region := range regionNames {
		slug, err := table.Slug(region)
		if err != nil {
			return DeployDescriptor{}, fmt.Errorf("region %q: %w", region, err)
		}
		appendScope(regional, region, slug)
	}
	// The global slice is region-less: empty slug drops the region segment from
	// the host FQDN, matching the global host's DNS record.
	appendScope(global, pki.ScopeGlobal, "")

	return desc, nil
}
