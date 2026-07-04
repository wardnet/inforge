package hetzner

import (
	"fmt"
	"sort"

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

// Realize installs and configures nginx on the ingress host. It runs the install
// script once, writes the rendered nginx.conf, validates it, and reloads nginx.
// Every step is idempotent and re-runnable: a route change rewrites the single
// config and reloads without reinstalling. It is invoked once per ingress host;
// hostKey (the canonical compute specKey) names the Pulumi command resources.
// backendIPs maps a service to its backend host's private-IP output for cross-host
// routes; the config is rendered inside an apply over them so the rendered upstream
// addresses are the resolved private IPs.
func (h *HetznerTLS) Realize(
	ctx *pulumi.Context,
	hostKey string,
	host types.ComputeOutputs,
	deployUser string,
	routes []types.IngressRoute,
	apps []types.IngressApp,
	health []types.IngressHealth,
	healthPort int,
	gateways []types.IngressGateway,
	backendIPs map[string]pulumi.StringOutput,
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

	writeScript, err := h.renderWriteScript(hostKey, routes, apps, health, healthPort, gateways, backendIPs)
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
	//    Delete the file). writeScript embeds the full config, so it is also the
	//    change trigger for this step and the reload below.
	cfg, err := remote.NewCommand(ctx, base+"-config", &remote.CommandArgs{
		Connection: conn,
		Create:     writeScript,
		Update:     writeScript,
		Triggers:   pulumi.Array{writeScript},
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
		Triggers:   pulumi.Array{writeScript},
	}, pulumi.DependsOn([]pulumi.Resource{cfg})); err != nil {
		return fmt.Errorf("ingress %q: reload nginx: %w", hostKey, err)
	}

	return nil
}

// renderWriteScript builds the shell that writes the rendered nginx.conf. Apps
// carry no backend, so they always render synchronously. When every route is also
// co-located (no cross-host backends), the whole config renders synchronously and
// returns a plain string. When some routes are cross-host, it renders inside an
// apply over the backend private-IP outputs, substituting each cross-host route's
// Backend with the resolved IP before Render — so the upstream addresses are the
// real private IPs, resolved at deploy time. In preview a cross-host backend IP is
// unknown, so the apply (and the command that consumes it) is skipped entirely.
func (h *HetznerTLS) renderWriteScript(hostKey string, routes []types.IngressRoute, apps []types.IngressApp, health []types.IngressHealth, healthPort int, gateways []types.IngressGateway, backendIPs map[string]pulumi.StringOutput) (pulumi.StringInput, error) {
	if len(backendIPs) == 0 {
		cfg, err := nginx.Render(routes, apps, health, healthPort, gateways)
		if err != nil {
			return nil, err
		}
		return pulumi.String(iremote.WriteFileScript(nginx.ConfigPath, cfg)), nil
	}

	svcNames := make([]string, 0, len(backendIPs))
	for n := range backendIPs {
		svcNames = append(svcNames, n)
	}
	sort.Strings(svcNames)
	outs := make([]any, len(svcNames))
	for i, n := range svcNames {
		outs[i] = backendIPs[n]
	}
	return pulumi.All(outs...).ApplyT(func(args []any) (string, error) {
		resolved := make(map[string]string, len(svcNames))
		for i, n := range svcNames {
			resolved[n], _ = args[i].(string)
		}
		rendered := make([]types.IngressRoute, len(routes))
		for i, r := range routes {
			if r.Backend == "" {
				ip := resolved[r.Service]
				if ip == "" {
					return "", fmt.Errorf("ingress %q: cross-host route for service %q has no resolved backend private IP", hostKey, r.Service)
				}
				r.Backend = ip
			}
			rendered[i] = r
		}
		renderedHealth := make([]types.IngressHealth, len(health))
		for i, hh := range health {
			if hh.Backend == "" {
				ip := resolved[hh.Service]
				if ip == "" {
					return "", fmt.Errorf("ingress %q: cross-host health for service %q has no resolved backend private IP", hostKey, hh.Service)
				}
				hh.Backend = ip
			}
			renderedHealth[i] = hh
		}
		cfg, err := nginx.Render(rendered, apps, renderedHealth, healthPort, gateways)
		if err != nil {
			return "", err
		}
		return iremote.WriteFileScript(nginx.ConfigPath, cfg), nil
	}).(pulumi.StringOutput), nil
}
