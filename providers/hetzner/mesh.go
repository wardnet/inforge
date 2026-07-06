package hetzner

import (
	"fmt"
	"sort"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/meshnginx"
	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/nginx"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/types"
)

// HetznerMesh implements types.MeshProvider for Hetzner Cloud. It realizes a
// host's east-west mesh proxy (ADR-0032) — a SECOND nginx, separate from the
// north-south one (HetznerTLS) — by installing nginx over SSH (reusing the
// north-south install so the binary matches), writing a dedicated systemd unit
// (meshpaths.UnitName) and config (meshpaths.ConfigPath), seeding placeholder
// cert material so nginx starts before real leaves land, and reloading. Like
// HetznerTLS the rendering lives in internal/meshnginx and only the transport
// (command.remote over SSH) lives here.
type HetznerMesh struct {
	deployPrivateKey string
	slug             string
}

// NewMesh creates a HetznerMesh provider. slug names the Pulumi command
// resources; deployPrivateKey is the SSH key inforge connects with (empty in
// preview, where Realize skips the remote connection).
func NewMesh(deployPrivateKey, slug string) *HetznerMesh {
	return &HetznerMesh{deployPrivateKey: deployPrivateKey, slug: slug}
}

// Realize installs and configures the mesh nginx on one host. It installs nginx +
// the seed script + the unit once, writes the rendered mesh config (inside an
// apply that fills the listen address and each target's Addr from compute IPs),
// and reload-or-restarts the mesh unit. Every step is idempotent and re-runnable.
func (h *HetznerMesh) Realize(
	ctx *pulumi.Context,
	hostKey string,
	host types.ComputeOutputs,
	deployUser string,
	cfg meshnginx.Config,
	listenAddr pulumi.StringInput,
	targetIPs map[string]pulumi.StringOutput,
	env string,
	dependsOn []pulumi.Resource,
) error {
	if !ctx.DryRun() {
		if deployUser == "" {
			return fmt.Errorf("mesh %q: host has no deploy_user; inforge needs one to SSH and realize the mesh proxy", hostKey)
		}
		if h.deployPrivateKey == "" {
			return fmt.Errorf("mesh %q: no deploy private key configured (set the deploy_private_key stack config or INFORGE_DEPLOY_PRIVATE_KEY)", hostKey)
		}
	}

	conn := iremote.Connection(host.PublicIP, deployUser, h.deployPrivateKey)
	base := naming.Resource(env, h.slug, "mesh", hostKey)

	// 1. Install nginx (reuse the north-south install so the binary version matches)
	//    plus openssl for the placeholder seed. First per-host SSH command, so it
	//    carries dependsOn (the host's cloud-init readiness gate).
	install, err := remote.NewCommand(ctx, base+"-install", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(nginx.InstallScript() + "\nsudo -E apt-get install -y openssl\n"),
	}, pulumi.DependsOn(dependsOn))
	if err != nil {
		return fmt.Errorf("mesh %q: install nginx: %w", hostKey, err)
	}

	// 2. Write the seed script + the proxy's systemd unit, then daemon-reload. The
	//    unit's ExecStartPre chain locally decrypts + projects whatever real
	//    material already sits in the persistent leaf.age (inforge-agent
	//    mesh-project, `-`-prefixed) then seeds placeholders for anything still
	//    absent — so a reboot self-heals the tmpfs cert dir to real (or at worst
	//    placeholder) material rather than crash-looping (ADR-0035). Renewal is a
	//    push: `inforge pki renew` SSHes a fresh leaf.age directly to this host and
	//    signals reload-or-restart — there is no on-host renewal timer to enable.
	egressNames := make([]string, 0, len(cfg.Egress))
	for _, e := range cfg.Egress {
		egressNames = append(egressNames, e.Name)
	}
	unitScript := iremote.WriteFileScript(meshnginx.SeedScriptPath, meshnginx.SeedScript(egressNames)) + "\n" +
		iremote.WriteFileScript(meshnginx.UnitPath, meshnginx.UnitFile()) + "\n" +
		"sudo systemctl daemon-reload\n" +
		fmt.Sprintf("sudo systemctl enable %s\n", iremote.Quote(meshpaths.UnitName))
	unit, err := remote.NewCommand(ctx, base+"-unit", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(unitScript),
		Update:     pulumi.String(unitScript),
		Triggers:   pulumi.Array{pulumi.String(unitScript)},
	}, pulumi.DependsOn([]pulumi.Resource{install}))
	if err != nil {
		return fmt.Errorf("mesh %q: write mesh unit: %w", hostKey, err)
	}

	// 3. Write the rendered mesh config (inside an apply over the listen address +
	//    target IPs). In preview those outputs are unknown, so the apply — and the
	//    command consuming it — is skipped entirely, exactly like the ingress path.
	writeScript := h.renderWriteScript(hostKey, cfg, listenAddr, targetIPs)
	config, err := remote.NewCommand(ctx, base+"-config", &remote.CommandArgs{
		Connection: conn,
		Create:     writeScript,
		Update:     writeScript,
		Triggers:   pulumi.Array{writeScript},
	}, pulumi.DependsOn([]pulumi.Resource{unit}))
	if err != nil {
		return fmt.Errorf("mesh %q: write mesh config: %w", hostKey, err)
	}

	// 4. Validate + (re)start the mesh unit after the config is in place. The seed
	//    ExecStartPre guarantees valid material before nginx -t runs on start.
	const reload = "sudo " + nginxBinPath + " -t -c " + meshpaths.ConfigPath +
		" && sudo systemctl reload-or-restart " + meshpaths.UnitName
	if _, err := remote.NewCommand(ctx, base+"-reload", &remote.CommandArgs{
		Connection: conn,
		Create:     pulumi.String(reload),
		Update:     pulumi.String(reload),
		Triggers:   pulumi.Array{writeScript},
	}, pulumi.DependsOn([]pulumi.Resource{config})); err != nil {
		return fmt.Errorf("mesh %q: reload mesh nginx: %w", hostKey, err)
	}
	return nil
}

// nginxBinPath is the nginx binary the north-south install provides, used to
// validate the mesh config with its own -c before the reload.
const nginxBinPath = "/usr/sbin/nginx"

// renderWriteScript builds the shell that writes the rendered mesh config. It
// always renders inside an apply over the listen address and every target's IP
// output, substituting cfg.ListenAddr and each Target.Addr ("<ip>:MTLSPort")
// before Render — so the addresses are the real IPs resolved at deploy time. In
// preview the outputs are unknown, so the apply is skipped and no command runs.
func (h *HetznerMesh) renderWriteScript(hostKey string, cfg meshnginx.Config, listenAddr pulumi.StringInput, targetIPs map[string]pulumi.StringOutput) pulumi.StringInput {
	// Order the target IP outputs deterministically so the apply's arg slice is
	// stable; index 0 is always the listen address.
	names := make([]string, 0, len(targetIPs))
	for n := range targetIPs {
		names = append(names, n)
	}
	sort.Strings(names)
	outs := make([]any, 0, len(names)+1)
	outs = append(outs, listenAddr)
	for _, n := range names {
		outs = append(outs, targetIPs[n])
	}
	return pulumi.All(outs...).ApplyT(func(args []any) (string, error) {
		listen, _ := args[0].(string)
		resolved := make(map[string]string, len(names))
		for i, n := range names {
			resolved[n], _ = args[i+1].(string)
		}
		rendered := cfg
		rendered.ListenAddr = listen
		rendered.Targets = make([]meshnginx.Target, len(cfg.Targets))
		for i, t := range cfg.Targets {
			ip := resolved[t.Name]
			if ip == "" {
				return "", fmt.Errorf("mesh %q: target %q has no resolved host IP", hostKey, t.Name)
			}
			t.Addr = fmt.Sprintf("%s:%d", ip, meshpaths.MTLSPort)
			rendered.Targets[i] = t
		}
		out, err := meshnginx.Render(rendered)
		if err != nil {
			return "", err
		}
		return iremote.WriteFileScript(meshpaths.ConfigPath, out), nil
	}).(pulumi.StringOutput)
}
