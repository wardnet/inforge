// Package app models the host-side scaffolding inforge provisions for a static
// front-end (an `app` resource, ADR-0026): the on-host directory its bundles are
// delivered into, the `current` symlink nginx serves as the document root, and
// the per-environment deploy descriptor the release path (slice D) consumes.
//
// It mirrors internal/service for the app workload: a service targets a host and
// runs under systemd; an app targets an ingress and is served as files by that
// ingress's nginx. Provisioning (seed the folder + a placeholder bundle) is
// separate from delivery (pushing a real bundle at release time). The package is
// pure (no Pulumi/SSH): the program composes the host commands from these paths.
package app

import (
	"fmt"
	"sort"

	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// Folder returns the on-host directory an app's bundles are deployed into. Apps
// live under an `app/` segment so an app and a service of the same name never
// collide (services live directly under /srv/wardnet/<name>).
func Folder(name string) string {
	return "/srv/wardnet/app/" + name
}

// CurrentPath returns the `current` symlink under an app's folder — the document
// root nginx serves. Provisioning points it at the seeded placeholder; the
// release path (slice D) atomically swaps it to a delivered bundle directory.
func CurrentPath(name string) string {
	return Folder(name) + "/current"
}

// PlaceholderSubdir is the bundle directory name the placeholder is seeded into,
// relative to an app's Folder. `current` is symlinked to it until a real release
// lands, so the app's server block + ACME cert provision before the first bundle.
const PlaceholderSubdir = "placeholder"

// PlaceholderDir / PlaceholderIndexPath are the on-host paths of the seeded
// placeholder bundle and its index.html.
func PlaceholderDir(name string) string { return Folder(name) + "/" + PlaceholderSubdir }
func PlaceholderIndexPath(name string) string {
	return PlaceholderDir(name) + "/index.html"
}

// PlaceholderIndexHTML is the static page nginx serves before an app's first
// release. It is a plain 200 page (returning a literal 503 would need per-app
// nginx error-page logic, which buys nothing here): its job is purely to give the
// app a real document so the server block and its Let's Encrypt certificate
// provision ahead of the first bundle.
const PlaceholderIndexHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Not yet released</title></head>
<body style="font-family:system-ui,sans-serif;text-align:center;padding-top:4rem">
<h1>Not yet released</h1>
<p>This application has been provisioned but no version has been deployed yet.</p>
</body>
</html>
`

// DeployTarget is one app's deployment coordinates the release path resolves: the
// ingress host to push to (its SSH/cloud-init DNS name), the on-host folder a
// bundle is delivered into, the public FQDN it is served on, whether it is a SPA,
// and the account inforge connects as over SSH (the ingress host's deploy user).
type DeployTarget struct {
	App            string `yaml:"app"              json:"app"`
	IngressHostDNS string `yaml:"ingress_host_dns" json:"ingress_host_dns"`
	DeployPath     string `yaml:"deploy_path"      json:"deploy_path"`
	FQDN           string `yaml:"fqdn"             json:"fqdn"`
	Spa            bool   `yaml:"spa"              json:"spa"`
	// SSHUser is the account inforge connects as over SSH to deliver the bundle —
	// the ingress host's deploy_user. Falls back to the historical "deploy".
	SSHUser string `yaml:"ssh_user" json:"ssh_user"`
}

// defaultSSHUser is the connect-as account used when an ingress host declares no
// deploy_user (mirrors internal/service.defaultSSHUser).
const defaultSSHUser = "deploy"

// DeployDescriptor is the per-environment set of app deploy targets, derived
// purely from resolved resources (mirrors service.DeployDescriptor).
type DeployDescriptor struct {
	Environment string         `yaml:"environment" json:"environment"`
	Targets     []DeployTarget `yaml:"targets"     json:"targets"`
}

// BuildDeployDescriptor derives the app deploy descriptor for an environment from
// its single shared (regional) resource set, instantiated into every region in
// the table. Each app expands into one DeployTarget per region — the region slug
// makes each target's FQDN and ingress host DNS distinct — so a single-region
// environment is unchanged while a multi-region one fans an app across regions.
// Regions are iterated in sorted order so the targets are stable across runs. An
// app's ingress host DNS is the HostFQDN of the compute its ingress references; an
// app whose ingress (or that ingress's host) does not resolve is skipped — the
// realization side validates resolution, so the skip is purely defensive.
func BuildDeployDescriptor(env, baseDomain string, res types.Resources, table regions.Table) (DeployDescriptor, error) {
	desc := DeployDescriptor{Environment: env}
	regionNames := make([]string, 0, len(table))
	for region := range table {
		regionNames = append(regionNames, region)
	}
	sort.Strings(regionNames)

	ingressHostName := ingressHostNamesByApp(res)
	deployUsers := naming.DeployUsersByHost(res.Compute)
	canonical := naming.CanonicalComputeKeys(res.Compute)
	for _, region := range regionNames {
		slug, err := table.Slug(region)
		if err != nil {
			return DeployDescriptor{}, fmt.Errorf("region %q: %w", region, err)
		}
		for _, a := range res.App {
			hostName, ok := ingressHostName[a.Name]
			if !ok {
				continue
			}
			sshUser := deployUsers[canonical[hostName]]
			if sshUser == "" {
				sshUser = defaultSSHUser
			}
			desc.Targets = append(desc.Targets, DeployTarget{
				App:            a.Name,
				IngressHostDNS: naming.HostFQDN(env, slug, hostName, baseDomain),
				DeployPath:     Folder(a.Name),
				FQDN:           naming.AppFQDN(a.Subdomain, slug, baseDomain),
				Spa:            a.Spa,
				SSHUser:        sshUser,
			})
		}
	}
	return desc, nil
}

// ingressHostNamesByApp maps each app name to the bare compute name its ingress
// references (app.Ingress -> ingress.Host). An app whose ingress is unknown is
// absent.
func ingressHostNamesByApp(res types.Resources) map[string]string {
	hostByIngress := map[string]string{}
	for _, ing := range res.Ingress {
		hostByIngress[ing.Name] = ing.Host
	}
	out := map[string]string{}
	for _, a := range res.App {
		if host, ok := hostByIngress[a.Ingress]; ok {
			out[a.Name] = host
		}
	}
	return out
}

// Marshal renders the app deploy descriptor as YAML for the release workflow.
func (d DeployDescriptor) Marshal() ([]byte, error) {
	b, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshal app deploy descriptor: %w", err)
	}
	return b, nil
}
