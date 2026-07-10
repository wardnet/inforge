package meshplan

import (
	"fmt"

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
	Host    string `yaml:"host" json:"host" pulumi:"host"`
	HostDNS string `yaml:"host_dns" json:"host_dns" pulumi:"host_dns"`
	SSHUser string `yaml:"ssh_user" json:"ssh_user" pulumi:"ssh_user"`
	// Scope is the host's mesh scope: its region name, or pki.ScopeGlobal.
	Scope string `yaml:"scope" json:"scope" pulumi:"scope"`
}

// DeployDescriptor is the `meshDeployDescriptor` stack output (the mesh sibling
// of deployDescriptor / appDeployDescriptor).
//
// See service.DeployDescriptor's doc comment: every field here MUST also carry
// a `pulumi:"..."` tag — this type is exported via ctx.Export(pulumi.Any(...)),
// and the Go SDK's struct marshaler drops any field lacking that tag.
type DeployDescriptor struct {
	Environment string         `yaml:"environment" json:"environment" pulumi:"environment"`
	Targets     []DeployTarget `yaml:"targets" json:"targets" pulumi:"targets"`
}

// BuildDeployDescriptor derives the mesh deploy descriptor: one target per mesh
// host (a host running ≥1 pki: service), across every region plus the global
// scope. Derived purely from resolved resources — no cloud outputs — like its
// service/app siblings.
func BuildDeployDescriptor(env, baseDomain string, regional, global types.Resources, table regions.Table) (DeployDescriptor, error) {
	desc := DeployDescriptor{Environment: env}

	appendScope := func(res types.Resources, scope, slug string) {
		canonical := naming.CanonicalComputeKeys(res.Compute)
		deployUsers := naming.DeployUsersByHost(res.Compute)
		// Union of service hosts AND the gateway's host: a gateway-only host runs
		// a mesh proxy too, and the deploy baseline must be able to trigger its
		// pull — this is the third consumer of the shared grouping (rule
		// mesh-host-grouping-is-single-sourced), alongside realizeMesh and the
		// renew core.
		byHost := ServicesByHost(res, canonical)
		gwByHost := GatewayMemberByHost(res, canonical)
		for _, hostKey := range UnionHostKeys(byHost, gwByHost) {
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

	for _, region := range table.SortedNames() {
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
