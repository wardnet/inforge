package hetzner

import (
	"fmt"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/nginx"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/types"
)

// HetznerTLS implements types.IngressProvider for Hetzner Cloud. It
// realizes a tls-termination spec by installing nginx (plus the native ACME
// module) on the host over SSH and writing one nginx.conf that covers every
// service's typed ingress entries — http servers that terminate ACME TLS and
// reverse-proxy to local services, and stream servers that L4-forward raw
// connections with the PROXY protocol. nginx is purely a Hetzner realization
// detail — the rendering lives in internal/nginx and the transport
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

// Realize installs and configures nginx on the host. It runs the install script
// once, writes the rendered nginx.conf, validates it, and reloads nginx. Every
// step is idempotent and re-runnable: a route change rewrites the single config
// and reloads without reinstalling. It is invoked once per host with ingress;
// hostKey (the canonical compute specKey) names the Pulumi command resources.
func (h *HetznerTLS) Realize(
	ctx *pulumi.Context,
	hostKey string,
	host types.ComputeOutputs,
	deployUser string,
	routes []types.IngressRoute,
	env string,
	dependsOn []pulumi.Resource,
) error {
	// Both connection requirements are enforced only at up time. During preview
	// the command.remote resources never connect (no Create on a dry run), so a
	// missing deploy user or key is harmless and preview must still succeed.
	// deploy_user is also validated up front (an ingress host must declare one),
	// so this guard is a backstop.
	if !ctx.DryRun() {
		if deployUser == "" {
			return fmt.Errorf("ingress %q: host has no deploy_user; inforge needs one to SSH and realize the ingress proxy", hostKey)
		}
		if h.deployPrivateKey == "" {
			return fmt.Errorf("ingress %q: no deploy private key configured (set the deploy_private_key stack config or INFORGE_DEPLOY_PRIVATE_KEY)", hostKey)
		}
	}

	configContent, err := nginx.Render(routes)
	if err != nil {
		return fmt.Errorf("ingress %q: %w", hostKey, err)
	}

	conn := iremote.Connection(host.PublicIP, deployUser, h.deployPrivateKey)
	base := naming.Resource(env, h.slug, "ingress", hostKey)

	// 1. Install nginx + the ACME module. First per-host SSH command, so it carries
	//    dependsOn (the host's cloud-init readiness gate); later steps chain off it.
	install, err := remote.NewCommand(ctx, base+"-install", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(nginx.InstallScript()),
	}, pulumi.DependsOn(dependsOn))
	if err != nil {
		return fmt.Errorf("ingress %q: install nginx: %w", hostKey, err)
	}

	// 2. Write the rendered nginx.conf. Update in place on a content change so a
	//    Triggers change rewrites rather than replacing (which would Create AND
	//    Delete the file).
	writeScript := iremote.WriteFileScript(nginx.ConfigPath, configContent)
	cfg, err := remote.NewCommand(ctx, base+"-config", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(writeScript),
		Update:     pulumi.String(writeScript),
		Triggers:   pulumi.Array{pulumi.String(configContent)},
	}, pulumi.DependsOn([]pulumi.Resource{install}))
	if err != nil {
		return fmt.Errorf("ingress %q: write nginx.conf: %w", hostKey, err)
	}

	// 3. Validate and reload after the config is in place; re-runs on any change.
	//    `nginx -t` fails the deploy loudly on a bad config rather than reloading
	//    into a broken state.
	const reload = "sudo nginx -t && sudo systemctl reload-or-restart nginx"
	if _, err := remote.NewCommand(ctx, base+"-reload", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(reload),
		Update:     pulumi.String(reload),
		Triggers:   pulumi.Array{pulumi.String(configContent)},
	}, pulumi.DependsOn([]pulumi.Resource{cfg})); err != nil {
		return fmt.Errorf("ingress %q: reload nginx: %w", hostKey, err)
	}

	return nil
}
