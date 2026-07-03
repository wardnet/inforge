// Package service models the host-side scaffolding inforge provisions for a
// service — a per-service folder and an inforge-managed systemd unit — and
// derives the per-environment deploy descriptor the reusable deployment
// workflow consumes. Provisioning (folder + unit) is separate from deployment
// (delivering the code); see docs/adr/0007.
package service

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wardnet/inforge/internal/hostpaths"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// Folder returns the on-host directory a service's payload is deployed into.
func Folder(name string) string {
	return "/srv/wardnet/" + name
}

// UnitName returns the systemd unit name inforge manages for a service. The
// scheme lives in internal/hostpaths so inforge-agent (which reloads the
// unit at renewal) shares one definition.
func UnitName(name string) string {
	return hostpaths.UnitName(name)
}

// UnitPath returns the absolute on-host path of a service's systemd unit file.
func UnitPath(name string) string {
	return "/etc/systemd/system/" + UnitName(name)
}

// AgentBin is the on-host path of the inforge-agent binary, the
// ExecStart for every service unit. inforge deploy downloads it here.
const AgentBin = hostpaths.AgentBin

// DescriptorDir returns the per-service directory holding the agent's
// inputs (descriptor.yaml + credential.age). It is the single argument passed to
// inforge-agent in the unit's ExecStart.
func DescriptorDir(name string) string {
	return "/etc/wardnet/services/" + name
}

// ExecPath returns the real service binary the agent execs after dropping
// privilege — the payload `inforge release` delivers into the service folder. It
// is what the descriptor's `exec` field is set to, kept in sync with the unit's
// WorkingDirectory here.
func ExecPath(name string) string {
	return Folder(name) + "/run"
}

// DescriptorPath / CredentialPath are the two files inforge writes into a
// service's DescriptorDir for the agent to read: the versioned, secret-free
// descriptor (0644) and the host-key-encrypted provider credential (0600). The
// filenames must match the agent's descriptorFile/credentialFile constants.
func DescriptorPath(name string) string {
	return DescriptorDir(name) + "/descriptor.yaml"
}

// CredentialPath returns the on-host path of a service's age-encrypted credential.
func CredentialPath(name string) string {
	return DescriptorDir(name) + "/credential.age"
}

// unitTemplate renders the unit. The service runs as root under inforge-agent
// (no User=): the agent fetches secrets, then drops privilege to the
// service user itself and execs ExecPath, so systemd supervises the real service
// PID. StartLimitIntervalSec=0 disables systemd's start-rate limit so a service
// always recovers once the vault returns (the agent bounds its own retry
// backoff per start, then exits non-zero to let Restart=on-failure loop).
const unitTemplate = `[Unit]
Description=wardnet %s
After=network.target
StartLimitIntervalSec=0

[Service]
Type=simple
WorkingDirectory=%s
RuntimeDirectory=%s
RuntimeDirectoryMode=0700
ExecStart=%s %s
%sRestart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// Unit renders the systemd unit file for a service. The unit's ExecStart is the
// inforge-agent binary pointed at the service's descriptor directory; the
// unit runs as root (no User=) because the agent drops privilege itself.
// RuntimeDirectory= gives the agent a tmpfs dir (RuntimeDir) to project
// mesh PEMs into, created and cleaned with the unit. ExecReload= is emitted when
// the service declares reload:, so the renewal timer can apply a renewed leaf
// without a restart.
func Unit(spec types.ServiceSpec) string {
	reloadLine := ""
	if spec.Reload != "" {
		reloadLine = "ExecReload=" + spec.Reload + "\n"
	}
	return fmt.Sprintf(unitTemplate, spec.Name, Folder(spec.Name), hostpaths.RuntimeSubdir(spec.Name), AgentBin, DescriptorDir(spec.Name), reloadLine)
}

// RuntimeDir is the tmpfs directory the agent projects a service's mesh
// PEMs into. It matches the unit's RuntimeDirectory= (RuntimeSubdir) and is
// shared with inforge-agent via internal/hostpaths.
func RuntimeDir(name string) string {
	return hostpaths.RuntimeDir(name)
}

// RenewUnitName / RenewTimerName name the per-service renewal oneshot + timer
// that re-project the current mesh leaf and reload the unit on change.
func RenewUnitName(name string) string  { return "wardnet-" + name + "-renew.service" }
func RenewTimerName(name string) string { return "wardnet-" + name + "-renew.timer" }

// RenewUnitPath / RenewTimerPath are the on-host paths of those units.
func RenewUnitPath(name string) string  { return "/etc/systemd/system/" + RenewUnitName(name) }
func RenewTimerPath(name string) string { return "/etc/systemd/system/" + RenewTimerName(name) }

const renewServiceTemplate = `[Unit]
Description=wardnet %s mesh certificate renewal

[Service]
Type=oneshot
ExecStart=%s project %s
`

// RenewService renders the oneshot that runs `inforge-agent project` for a
// service: re-fetch the current leaf, re-project it into the RuntimeDir, and
// reload-or-restart the unit if it changed.
func RenewService(spec types.ServiceSpec) string {
	return fmt.Sprintf(renewServiceTemplate, spec.Name, AgentBin, DescriptorDir(spec.Name))
}

const renewTimerTemplate = `[Unit]
Description=wardnet %s mesh certificate renewal timer

[Timer]
OnCalendar=daily
RandomizedDelaySec=1h
Persistent=true

[Install]
WantedBy=timers.target
`

// RenewTimer renders the timer that fires the renewal oneshot daily (with a
// randomized delay), well inside the 90-day leaf TTL.
func RenewTimer(spec types.ServiceSpec) string {
	return fmt.Sprintf(renewTimerTemplate, spec.Name)
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
// its single shared resource set, instantiated into every region in the table.
// Each service expands into one DeployTarget per region — the region slug makes
// each target's host DNS distinct — so a single-region environment is unchanged
// while a multi-region one fans a service out across every region's host.
// Regions are iterated in sorted order so the targets are stable across runs. A
// service's host DNS is the domain of the DNS record pointing at its host compute
// instance; if the host has no DNS record, the compute name is used as the
// subdomain.
func BuildDeployDescriptor(env, baseDomain string, res types.Resources, table regions.Table) (DeployDescriptor, error) {
	desc := DeployDescriptor{Environment: env}
	regionNames := make([]string, 0, len(table))
	for region := range table {
		regionNames = append(regionNames, region)
	}
	sort.Strings(regionNames)

	canonical := naming.CanonicalComputeKeys(res.Compute)
	deployUsers := naming.DeployUsersByHost(res.Compute)
	for _, region := range regionNames {
		slug, err := table.Slug(region)
		if err != nil {
			return DeployDescriptor{}, fmt.Errorf("region %q: %w", region, err)
		}
		for _, svc := range res.Service {
			hostDNS := hostDNS(svc.Host, env, baseDomain, slug)
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

// hostDNS computes the fully-qualified SSH/cloud-init domain for a host compute
// specKey: "<compute>.vm.<env>.<slug>.<baseDomain>", derived deterministically
// from the bare compute name (the "vm"-segment host record).
func hostDNS(hostKey, env, baseDomain, slug string) string {
	return naming.HostFQDN(env, slug, computeName(hostKey), baseDomain)
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
