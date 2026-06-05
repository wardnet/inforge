// Package service models the host-side scaffolding inforge provisions for a
// service — a per-service folder and an inforge-managed systemd unit — and
// derives the per-environment deploy descriptor the reusable deployment
// workflow consumes. Provisioning (folder + unit) is separate from deployment
// (delivering the code); see docs/adr/0007.
package service

import (
	"fmt"
	"strings"

	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// Folder returns the on-host directory a service's payload is deployed into.
func Folder(name string) string {
	return "/srv/wardnet/" + name
}

// UnitName returns the systemd unit name inforge manages for a service.
func UnitName(name string) string {
	return "wardnet-" + name + ".service"
}

// UnitPath returns the absolute on-host path of a service's systemd unit file.
func UnitPath(name string) string {
	return "/etc/systemd/system/" + UnitName(name)
}

const unitHead = `[Unit]
Description=wardnet %s
After=network.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s/run
`

const unitTail = `Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// Unit renders the systemd unit file for a service. When spec.User is set a
// User= directive is included; otherwise the unit inherits the invoking user.
func Unit(spec types.ServiceSpec) string {
	folder := Folder(spec.Name)
	head := fmt.Sprintf(unitHead, spec.Name, folder, folder)
	if spec.User != "" {
		return head + "User=" + spec.User + "\n" + unitTail
	}
	return head + unitTail
}

// DeployTarget is one service's deployment coordinates: where to push the
// payload (the host DNS name), the folder to extract it into, the unit to
// restart, and the optional no-login system user the service runs as.
type DeployTarget struct {
	Service string `yaml:"service"  json:"service"`
	HostDNS string `yaml:"host_dns" json:"host_dns"`
	Folder  string `yaml:"folder"   json:"folder"`
	Unit    string `yaml:"unit"     json:"unit"`
	// User is the no-login system user the service runs as. Empty when the
	// service spec declares no user. inforge deploy creates this user when
	// provisioning the unit.
	User string `yaml:"user,omitempty" json:"user,omitempty"`
	// SSHUser is the account inforge connects as over SSH to deliver the payload
	// — the host's deploy_user. It is DISTINCT from User (the no-login account
	// the service process runs as): they coincide only when the deploy user is
	// literally named the same. Falls back to "deploy" when the host declares no
	// deploy_user.
	SSHUser string `yaml:"ssh_user" json:"ssh_user"`
}

// defaultSSHUser is the connect-as account used when a service's host declares
// no deploy_user (preserves the historical hardcoded "deploy").
const defaultSSHUser = "deploy"

// DeployDescriptor is the per-environment set of deploy targets, derived purely
// from resolved resources.
type DeployDescriptor struct {
	Environment string         `yaml:"environment" json:"environment"`
	Targets     []DeployTarget `yaml:"targets"     json:"targets"`
}

// BuildDeployDescriptor derives the deploy descriptor for an environment from
// its resolved resources (keyed by region). A service's host DNS is the domain
// of the DNS record pointing at its host compute instance; if the host has no
// DNS record, the compute name is used as the subdomain.
func BuildDeployDescriptor(env, baseDomain string, byRegion map[string]types.Resources, table regions.Table) (DeployDescriptor, error) {
	desc := DeployDescriptor{Environment: env}
	for region, res := range byRegion {
		slug, err := table.Slug(region)
		if err != nil {
			return DeployDescriptor{}, fmt.Errorf("region %q: %w", region, err)
		}
		canonical := naming.CanonicalComputeKeys(res.Compute)
		deployUsers := deployUsersByHost(res.Compute)
		for _, svc := range res.Service {
			hostDNS := hostDNS(svc.Host, env, baseDomain, slug, res.DNS)
			sshUser := deployUsers[canonical[svc.Host]]
			if sshUser == "" {
				sshUser = defaultSSHUser
			}
			desc.Targets = append(desc.Targets, DeployTarget{
				Service: svc.Name,
				HostDNS: hostDNS,
				Folder:  Folder(svc.Name),
				Unit:    UnitName(svc.Name),
				User:    svc.User,
				SSHUser: sshUser,
			})
		}
	}
	return desc, nil
}

// deployUsersByHost maps each expanded compute specKey to its deploy_user (empty
// when the compute declares none).
func deployUsersByHost(computes []types.ComputeSpec) map[string]string {
	byHost := map[string]string{}
	for _, c := range computes {
		if c.DeployUser == nil {
			continue
		}
		for i := 1; i <= c.InstanceCount; i++ {
			byHost[naming.SpecKey(c.Name, i)] = c.DeployUser.Name
		}
	}
	return byHost
}

// hostDNS computes the fully-qualified domain for a host compute specKey:
// "<subdomain>.<env>.<slug>.<baseDomain>", where subdomain comes from the DNS
// record targeting that compute, falling back to the compute name.
func hostDNS(hostKey, env, baseDomain, slug string, dns []types.DnsSpec) string {
	subdomain := computeName(hostKey)
	for _, d := range dns {
		if d.Compute == hostKey {
			subdomain = d.Subdomain
			break
		}
	}
	return naming.RecordFQDN(env, slug, subdomain, baseDomain)
}

// computeName strips the "-NN" instance suffix from an expanded compute specKey.
func computeName(specKey string) string {
	if i := strings.LastIndex(specKey, "-"); i > 0 {
		return specKey[:i]
	}
	return specKey
}

// Marshal renders the deploy descriptor as YAML for the deployment workflow.
func (d DeployDescriptor) Marshal() ([]byte, error) {
	b, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshal deploy descriptor: %w", err)
	}
	return b, nil
}
