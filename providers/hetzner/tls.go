package hetzner

import (
	"fmt"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/caddy"
	"github.com/wardnet/inforge/internal/naming"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/types"
)

// HetznerTLS implements types.TLSTerminationProvider for Hetzner Cloud. It
// realizes a tls-termination spec by installing Caddy on the host over SSH and
// writing one reverse-proxy vhost per service. Caddy is purely a Hetzner
// realization detail — the rendering lives in internal/caddy and the transport
// (command.remote over SSH, via internal/remote) lives here.
type HetznerTLS struct {
	// deployPrivateKey is the private half of the env's deploy keypair. It is
	// transport only: it authenticates the SSH connection to the host (which
	// trusts the public half via provision.sh). It encrypts nothing.
	deployPrivateKey string
	slug             string
}

// NewTLS creates a HetznerTLS provider. slug is the region slug used to name the
// Pulumi command resources; deployPrivateKey is the SSH key inforge connects
// with. When deployPrivateKey is empty (e.g. preview), Realize skips the remote
// connection rather than attempting an unauthenticated one.
func NewTLS(deployPrivateKey, slug string) *HetznerTLS {
	return &HetznerTLS{
		deployPrivateKey: deployPrivateKey,
		slug:             slug,
	}
}

// Realize installs and configures Caddy on the terminator's host. It runs the
// install script once, writes the base Caddyfile, writes one vhost per service,
// and reloads Caddy. Every step is idempotent and re-runnable: adding a service
// adds a vhost file and reloads; removing one deletes the file and reloads.
func (h *HetznerTLS) Realize(
	ctx *pulumi.Context,
	spec types.TLSTerminationSpec,
	host types.ComputeOutputs,
	deployUser string,
	routes []types.TLSRoute,
	env string,
	dependsOn []pulumi.Resource,
) error {
	if spec.Provider != "hetzner" {
		return fmt.Errorf("hetzner provider received tls-termination spec with provider=%q", spec.Provider)
	}
	// Both connection requirements are enforced only at up time. During preview
	// the command.remote resources never connect (no Create on a dry run), so a
	// missing deploy user or key is harmless and preview must still succeed.
	// deploy_user is also validated up front (a terminator's host must declare
	// one), so this guard is a backstop.
	if !ctx.DryRun() {
		if deployUser == "" {
			return fmt.Errorf("tls-termination %q: host has no deploy_user; inforge needs one to SSH and realize the terminator", spec.Name)
		}
		if h.deployPrivateKey == "" {
			return fmt.Errorf("tls-termination %q: no deploy private key configured (set the deploy_private_key stack config or INFORGE_DEPLOY_PRIVATE_KEY)", spec.Name)
		}
	}

	conn := iremote.Connection(host.PublicIP, deployUser, h.deployPrivateKey)

	base := naming.Resource(env, h.slug, "tls", spec.Name)

	// Hosts with any passthrough/catch-all route need the layer4 realization
	// (path B); terminate-only hosts keep the simpler Caddyfile/conf.d path (A).
	if caddy.NeedsL4(routes) {
		return h.realizeL4(ctx, spec, conn, base, routes, dependsOn)
	}

	caddyfileContent := caddy.Caddyfile()

	// 1. Install Caddy and prepare conf.d. This is the first per-host SSH command,
	//    so it carries dependsOn (the host's cloud-init readiness gate); every
	//    later step chains off it, so the whole realization waits on the gate.
	install, err := remote.NewCommand(ctx, base+"-install", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(caddy.InstallScript()),
	}, pulumi.DependsOn(dependsOn))
	if err != nil {
		return fmt.Errorf("tls-termination %q: install caddy: %w", spec.Name, err)
	}

	// 2. Write the base Caddyfile (imports conf.d/*.caddy). Reloads triggered
	//    separately, so a content change re-writes and the reload step re-runs.
	//    Update (not replace) on a content change so it rewrites in place.
	caddyfileScript := iremote.WriteFileScript(caddy.CaddyfilePath, caddyfileContent)
	caddyfile, err := remote.NewCommand(ctx, base+"-caddyfile", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(caddyfileScript),
		Update:     pulumi.String(caddyfileScript),
		Triggers:   pulumi.Array{pulumi.String(caddyfileContent)},
	}, pulumi.DependsOn([]pulumi.Resource{install}))
	if err != nil {
		return fmt.Errorf("tls-termination %q: write Caddyfile: %w", spec.Name, err)
	}

	// 3. Write one vhost per service. Each is its own resource so adding or
	//    removing a service maps to creating or deleting exactly one file.
	configDeps := make([]pulumi.Resource, 1, 1+len(routes))
	configDeps[0] = caddyfile
	reloadTriggers := make(pulumi.Array, 1, 1+len(routes))
	reloadTriggers[0] = pulumi.String(caddyfileContent)
	for _, v := range routes {
		content := caddy.Vhost(v)
		writeScript := iremote.WriteFileScript(caddy.VhostPath(v.Service), content)
		cmd, vErr := remote.NewCommand(ctx, base+"-vhost-"+v.Service, &remote.CommandArgs{
			Connection: conn,
			Create:     pulumi.String(writeScript),
			// Update in place on a content change; without it a Triggers change
			// would replace, running Create AND Delete on the same vhost file.
			Update:   pulumi.String(writeScript),
			Delete:   pulumi.String(iremote.DeleteFileScript(caddy.VhostPath(v.Service))),
			Triggers: pulumi.Array{pulumi.String(content)},
		}, pulumi.DependsOn([]pulumi.Resource{caddyfile}))
		if vErr != nil {
			return fmt.Errorf("tls-termination %q: write vhost for %q: %w", spec.Name, v.Service, vErr)
		}
		configDeps = append(configDeps, cmd)
		reloadTriggers = append(reloadTriggers, pulumi.String(content))
	}

	// 4. Reload Caddy after all config is in place. Triggers on every config's
	//    content so the reload re-runs whenever any vhost or the Caddyfile
	//    changes — without replacing the install.
	if _, err := remote.NewCommand(ctx, base+"-reload", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String("sudo systemctl reload-or-restart caddy"),
		Update:     pulumi.String("sudo systemctl reload-or-restart caddy"),
		Triggers:   reloadTriggers,
	}, pulumi.DependsOn(configDeps)); err != nil {
		return fmt.Errorf("tls-termination %q: reload caddy: %w", spec.Name, err)
	}

	return nil
}

// realizeL4 is the layer4 realization (path B) for hosts with passthrough or
// catch-all routes. It installs a layer4-capable Caddy pointed at a native-JSON
// config, writes that config, and reloads. A single JSON file expresses the
// whole routing table (terminate, passthrough, catch-all), so unlike path A
// there is no per-service conf.d file — one config resource, re-runnable in place.
func (h *HetznerTLS) realizeL4(
	ctx *pulumi.Context,
	spec types.TLSTerminationSpec,
	conn remote.ConnectionArgs,
	base string,
	routes []types.TLSRoute,
	dependsOn []pulumi.Resource,
) error {
	jsonContent, err := caddy.RenderL4Config(routes)
	if err != nil {
		return fmt.Errorf("tls-termination %q: %w", spec.Name, err)
	}

	// 1. Install the layer4 Caddy + JSON-config systemd override. First per-host
	//    SSH command, so it carries the cloud-init readiness gate (dependsOn).
	install, err := remote.NewCommand(ctx, base+"-install", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(caddy.InstallScriptL4()),
	}, pulumi.DependsOn(dependsOn))
	if err != nil {
		return fmt.Errorf("tls-termination %q: install layer4 caddy: %w", spec.Name, err)
	}

	// 2. Write the native-JSON config. Update in place on a content change.
	writeScript := iremote.WriteFileScript(caddy.L4ConfigPath, jsonContent)
	cfg, err := remote.NewCommand(ctx, base+"-config", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(writeScript),
		Update:     pulumi.String(writeScript),
		Triggers:   pulumi.Array{pulumi.String(jsonContent)},
	}, pulumi.DependsOn([]pulumi.Resource{install}))
	if err != nil {
		return fmt.Errorf("tls-termination %q: write caddy.json: %w", spec.Name, err)
	}

	// 3. Reload Caddy after the config is in place; re-runs on any config change.
	if _, err := remote.NewCommand(ctx, base+"-reload", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String("sudo systemctl reload-or-restart caddy"),
		Update:     pulumi.String("sudo systemctl reload-or-restart caddy"),
		Triggers:   pulumi.Array{pulumi.String(jsonContent)},
	}, pulumi.DependsOn([]pulumi.Resource{cfg})); err != nil {
		return fmt.Errorf("tls-termination %q: reload caddy: %w", spec.Name, err)
	}

	return nil
}
