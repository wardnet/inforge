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
	"strings"

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

// BundleDir returns the on-host directory a specific release's bundle is
// extracted into — <Folder>/<sha>. The release path (slice D) delivers each SHA
// into its own directory and atomically swaps `current` to it; keeping every SHA
// in a sibling directory is exactly what makes a rollback a symlink repoint
// rather than a re-fetch from the store.
func BundleDir(name, sha string) string {
	return Folder(name) + "/" + sha
}

// KeepReleases is how many delivered bundle directories the release path retains
// on the ingress host (beyond the placeholder and whatever `current` points at),
// so a recent prior SHA can be rolled back to without re-fetching it. Older
// bundles are garbage-collected after each swap — see GCReleasesScript.
const KeepReleases = 5

// SwapCurrentScript renders the host shell that atomically repoints an app's
// `current` symlink at its <sha> bundle directory. It is the single source of
// truth for the swap contract both provisioning (slice C placeholder seed) and
// release (slice D) honour: a relative symlink staged under a temp name and
// renamed over `current` with `mv -T`, so one rename(2) flips the document root
// and nginx never observes a missing or half-written `current`. The target is the
// bare <sha> (relative to the app folder) so the link is independent of the
// folder's absolute location. See the atomic-current-swap rule in .agents/rules.
func SwapCurrentScript(name, sha string) string {
	folder := Folder(name)
	tmp := folder + "/.current.tmp"
	return fmt.Sprintf("sudo ln -sfn %s %s && sudo mv -T %s %s",
		shQuote(sha), shQuote(tmp), shQuote(tmp), shQuote(CurrentPath(name)))
}

// SeedCurrentScript renders the host shell that points `current` at the
// placeholder bundle, but only when nothing already occupies it (no file, no
// symlink — even a broken one). It is the provisioning counterpart to
// SwapCurrentScript: both keep the `current` symlink shape (a relative target) in
// this package per the atomic-current-swap rule, so a never-released app and a
// released one agree on the shape. The seed deliberately *creates* and never
// replaces — a re-provision must not clobber a release's `current`; the atomic
// replace is the release path's job (SwapCurrentScript).
func SeedCurrentScript(name string) string {
	current := CurrentPath(name)
	return fmt.Sprintf("if [ ! -e %s ] && [ ! -L %s ]; then sudo ln -s %s %s; fi",
		shQuote(current), shQuote(current), shQuote(PlaceholderSubdir), shQuote(current))
}

// GCReleasesScript renders the host shell that prunes delivered bundle
// directories beyond KeepReleases newest, never touching the placeholder or the
// directory `current` resolves to. `ls -1dt` orders newest-first so `tail` drops
// only the oldest surplus; `xargs -r` is a no-op when nothing is surplus. It is
// deliberately conservative — the live bundle and the placeholder are excluded by
// name so a GC can never remove the document root out from under nginx.
func GCReleasesScript(name string) string {
	folder := Folder(name)
	return fmt.Sprintf("cd %s && current=$(readlink current 2>/dev/null || true) && "+
		"ls -1dt */ 2>/dev/null | sed 's:/$::' | grep -vx %s | grep -vx current | grep -vx \"$current\" | "+
		"tail -n +%d | xargs -r sudo rm -rf",
		shQuote(folder), shQuote(PlaceholderSubdir), KeepReleases+1)
}

// shQuote single-quote-wraps s for safe interpolation into a host shell command,
// matching internal/remote.Quote without importing that (Pulumi-heavy) package —
// internal/app is deliberately free of Pulumi/SSH dependencies.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

// BuildDeployDescriptor derives the app deploy descriptor for an environment
// from its regional resource set, instantiated into every region in the
// table, plus its global resource set, instantiated once region-less. Each
// regional app expands into one DeployTarget per region — the region slug
// makes each target's FQDN and ingress host DNS distinct — so a single-region
// environment is unchanged while a multi-region one fans an app across
// regions. A global app expands into exactly one DeployTarget, with no
// region slug. Regions are iterated in sorted order so the targets are
// stable across runs. An app's ingress host DNS is the HostFQDN of the
// compute its ingress references; an app whose ingress (or that ingress's
// host) does not resolve is skipped — the realization side validates
// resolution, so the skip is purely defensive.
//
// ephemeralSlug is the ADR-0028 ephemeral-env exception threaded into each app's
// FQDN (see naming.AppFQDN): "" for a static env (unchanged URLs), the env's slug
// identity for an ephemeral env so its app hostname never collides with the
// source's. It does NOT affect the ingress host DNS, which is already env-scoped.
func BuildDeployDescriptor(env, baseDomain string, res, globalRes types.Resources, table regions.Table, ephemeralSlug string) (DeployDescriptor, error) {
	desc := DeployDescriptor{Environment: env}
	regionNames := make([]string, 0, len(table))
	for region := range table {
		regionNames = append(regionNames, region)
	}
	sort.Strings(regionNames)

	// appendScope mirrors internal/meshplan.BuildDeployDescriptor and
	// program.buildDBDeployDescriptor's identical regional+global closure
	// shape: derive ingressHostName/canonical/deployUsers fresh per scope
	// (regional or global) and append one DeployTarget per app, closing over
	// desc/env/baseDomain/ephemeralSlug instead of threading them through a
	// free function. An app whose ingress host doesn't resolve is skipped —
	// the realization side validates resolution, so the skip is purely
	// defensive.
	appendScope := func(scopeRes types.Resources, slug string) {
		ingressHostName := ingressHostNamesByApp(scopeRes)
		deployUsers := naming.DeployUsersByHost(scopeRes.Compute)
		canonical := naming.CanonicalComputeKeys(scopeRes.Compute)
		for _, a := range scopeRes.App {
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
				FQDN:           naming.AppFQDN(a.Subdomain, slug, baseDomain, ephemeralSlug),
				Spa:            a.Spa,
				SSHUser:        sshUser,
			})
		}
	}

	for _, region := range regionNames {
		slug, err := table.Slug(region)
		if err != nil {
			return DeployDescriptor{}, fmt.Errorf("region %q: %w", region, err)
		}
		appendScope(res, slug)
	}

	// The global slice is instantiated once, region-less (slug ""), mirroring
	// program.go's global scope (see its "global before regions" comment) — a
	// global app's ingress host DNS and FQDN are env-scoped only, with no
	// <slug> segment. Without this, a container: global app (e.g. the account
	// SPA, whose ingress is itself global) never lands in the descriptor,
	// even though `inforge deploy` fully realizes it.
	appendScope(globalRes, "")

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
