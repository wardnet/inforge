package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	hcloud "github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/cloudinit"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

// HetznerCompute implements types.ComputeProvider for Hetzner Cloud. One
// instance is shared per registry (i.e. per region). Firewalls and SSH keys
// are deduplicated across multiple Create calls via mutex-protected maps.
type HetznerCompute struct {
	sshAuthorizedKeys string
	deployPublicKey   string
	apiToken          string
	provider          *hcloud.Provider
	project           string
	slug              string
	eph               tags.Ephemeral
	mu                sync.Mutex
	firewalls         map[string]*hcloud.Firewall
	// sshKeys is keyed by env since SSH keys are env-scoped (not region-scoped).
	sshKeys map[string][]*hcloud.SshKey
	// instanceCounters tracks how many servers have been created for each
	// spec name so that Create assigns each call its unique specKey suffix.
	instanceCounters map[string]int
	regions          map[string]RegionConfig
}

// NewCompute creates a HetznerCompute provider. project is the inforge project
// name used to label cloud resources. slug is the region slug used for resource
// naming. eph carries the ADR-0028 ephemeral-env labels (zero value for a static
// env). regionOverrides is the output of ExtractRegionConfigs (the per-region
// realizations) and may be nil — Create then fails closed for any region that
// has no realization.
func NewCompute(sshAuthorizedKeys, deployPublicKey, apiToken string, provider *hcloud.Provider, project, slug string, eph tags.Ephemeral, regionOverrides map[string]RegionConfig) *HetznerCompute {
	if regionOverrides == nil {
		regionOverrides = map[string]RegionConfig{}
	}
	return &HetznerCompute{
		sshAuthorizedKeys: sshAuthorizedKeys,
		deployPublicKey:   deployPublicKey,
		apiToken:          apiToken,
		provider:          provider,
		project:           project,
		slug:              slug,
		eph:               eph,
		firewalls:         map[string]*hcloud.Firewall{},
		sshKeys:           map[string][]*hcloud.SshKey{},
		instanceCounters:  map[string]int{},
		regions:           regionOverrides,
	}
}

// Create provisions one Hetzner Cloud server for a compute spec instance. The
// program loop in program/program.go calls Create once per instance_count
// expansion; Create tracks the per-spec instance counter internally so each
// call receives its unique specKey name.
func (h *HetznerCompute) Create(
	ctx *pulumi.Context,
	spec types.ComputeSpec,
	network types.NetworkOutputs,
	env, abstractRegion, domain, manifest string,
	fwPorts types.FirewallPorts,
) (types.ComputeOutputs, error) {
	if spec.Provider != "hetzner" {
		return types.ComputeOutputs{}, fmt.Errorf("hetzner provider received spec with provider=%q", spec.Provider)
	}

	regionCfg, err := ResolveRegion(abstractRegion, h.regions)
	if err != nil {
		return types.ComputeOutputs{}, err
	}

	serverType, ok := regionCfg.ServerTypes[spec.Size]
	if !ok {
		return types.ComputeOutputs{}, fmt.Errorf("hetzner has no server type for size %q in region %q — add it to providers.hetzner.regions.%s.serverTypes", spec.Size, abstractRegion, abstractRegion)
	}

	resolvedImage, ok := regionCfg.Images[spec.Image]
	if !ok {
		return types.ComputeOutputs{}, fmt.Errorf("hetzner has no image for %q in region %q — add it to providers.hetzner.regions.%s.images", spec.Image, abstractRegion, abstractRegion)
	}

	fw, err := h.ensureFirewall(ctx, spec, env, fwPorts)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("ensure firewall: %w", err)
	}

	sshKeyList, err := h.ensureSshKeys(ctx, env)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("ensure ssh keys: %w", err)
	}

	// Advance this spec's instance counter under the lock.
	h.mu.Lock()
	h.instanceCounters[spec.Name]++
	instance := h.instanceCounters[spec.Name]
	h.mu.Unlock()

	serverName := naming.ResourceInstance(env, h.slug, "vm", spec.Name, instance)

	args := &hcloud.ServerArgs{
		Name:       pulumi.String(serverName),
		ServerType: pulumi.String(serverType),
		Image:      pulumi.String(resolvedImage),
		Location:   pulumi.String(regionCfg.Location),
		SshKeys: pulumi.StringArray{
			sshKeyList[0].Name,
			sshKeyList[1].Name,
		},
		FirewallIds: pulumi.IntArray{
			fw.ID().ApplyT(func(id pulumi.ID) (int, error) {
				return strconv.Atoi(string(id))
			}).(pulumi.IntOutput),
		},
		Networks: hcloud.ServerNetworkTypeArray{
			hcloud.ServerNetworkTypeArgs{
				SubnetId: network.SubnetID.ToStringPtrOutput(),
			},
		},
		Labels: toStringMap(tags.HetznerLabels(h.project, env, h.slug, spec.Container, h.eph)),
	}

	if spec.CloudInit != "" {
		var deployUserName string
		if spec.DeployUser != nil {
			deployUserName = spec.DeployUser.Name
		}
		userData, ciErr := cloudinit.Assemble(spec.CloudInit, cloudinit.Vars{
			Domain:          domain,
			DeployPublicKey: h.deployPublicKey,
			DeployUser:      deployUserName,
			Instance:        instance,
			Manifest:        manifest,
		})
		if ciErr != nil {
			return types.ComputeOutputs{}, fmt.Errorf("assemble cloud-init for %s: %w", serverName, ciErr)
		}
		args.UserData = pulumi.StringPtr(userData)
	}

	server, err := hcloud.NewServer(ctx, serverName, args, h.providerOpts()...)
	if err != nil {
		return types.ComputeOutputs{}, fmt.Errorf("create server %s: %w", serverName, err)
	}

	// The private IP is read back from the server's network attachment (we pin a
	// single SubnetId, so index 0 is that attachment). Hetzner assigns it when we
	// omit an explicit Ip; .Elem() unwraps the *string output to "" in preview.
	privateIP := server.Networks.Index(pulumi.Int(0)).Ip().Elem()

	return types.ComputeOutputs{PublicIP: server.Ipv4Address, PrivateIP: privateIP}, nil
}

// ensureFirewall returns the hcloud.Firewall for the spec name, creating it if it
// does not yet exist. The inbound rule set is derived, not hand-maintained: SSH
// (22) is always permitted from anywhere, the host's derived public ports (an
// ingress host's route listen ports, plus :80 when it terminates TLS) are opened
// to the internet, the derived private ports (a backend's route target ports) are
// opened only to the private network CIDR (reachable from a co-tenant ingress
// over the private network, never the internet), and any explicit
// spec.Firewall.Inbound rules are added on top as public. Duplicate (proto, port,
// source) tuples are collapsed so the rendered firewall is stable. It is safe to
// call concurrently.
func (h *HetznerCompute) ensureFirewall(ctx *pulumi.Context, spec types.ComputeSpec, env string, fwPorts types.FirewallPorts) (*hcloud.Firewall, error) {
	fwName := naming.Resource(env, h.slug, "fw", spec.Name)

	h.mu.Lock()
	defer h.mu.Unlock()

	if fw, ok := h.firewalls[fwName]; ok {
		return fw, nil
	}

	// publicSources is the internet; privateSources scopes a backend's target port
	// to the host's private network CIDR (validation guarantees an ingress fronting
	// it shares that network).
	publicSources := pulumi.StringArray{pulumi.String("0.0.0.0/0"), pulumi.String("::/0")}
	var privateSources pulumi.StringArray
	if fwPorts.PrivateSourceCIDR != "" {
		privateSources = pulumi.StringArray{pulumi.String(fwPorts.PrivateSourceCIDR)}
	}

	rules := make(hcloud.FirewallRuleArray, 0, len(fwPorts.Public)+len(fwPorts.Private)+4)
	// De-duplicate by (proto, port, scope) so a port that is both derived and
	// declared, or appears in two lists, is rendered once.
	seen := map[string]bool{}
	addTCP := func(port string, sources pulumi.StringArray, scope string) {
		key := "tcp/" + port + "/" + scope
		if seen[key] {
			return
		}
		seen[key] = true
		rules = append(rules, &hcloud.FirewallRuleArgs{
			Direction: pulumi.String("in"),
			Protocol:  pulumi.String("tcp"),
			Port:      pulumi.StringPtr(port),
			SourceIps: sources,
		})
	}
	// SSH first (management access is never locked out).
	addTCP("22", publicSources, "public")
	for _, p := range fwPorts.Public {
		addTCP(strconv.Itoa(p), publicSources, "public")
	}
	// Private rules only when a source CIDR is known; otherwise a backend port
	// would silently open to the internet.
	if privateSources != nil {
		for _, p := range fwPorts.Private {
			addTCP(strconv.Itoa(p), privateSources, "private")
		}
	}
	if spec.Firewall != nil {
		for _, r := range spec.Firewall.Inbound {
			key := r.Proto + "/" + string(r.Port) + "/public"
			if seen[key] {
				continue
			}
			seen[key] = true
			rule := &hcloud.FirewallRuleArgs{
				Direction: pulumi.String("in"),
				Protocol:  pulumi.String(r.Proto),
				SourceIps: publicSources,
			}
			if r.Proto != "icmp" {
				rule.Port = pulumi.StringPtr(string(r.Port))
			}
			rules = append(rules, rule)
		}
	}
	// Outbound: always allow all TCP, UDP, ICMP.
	rules = append(rules,
		&hcloud.FirewallRuleArgs{
			Direction:      pulumi.String("out"),
			Protocol:       pulumi.String("tcp"),
			DestinationIps: pulumi.StringArray{pulumi.String("0.0.0.0/0"), pulumi.String("::/0")},
		},
		&hcloud.FirewallRuleArgs{
			Direction:      pulumi.String("out"),
			Protocol:       pulumi.String("udp"),
			DestinationIps: pulumi.StringArray{pulumi.String("0.0.0.0/0"), pulumi.String("::/0")},
		},
		&hcloud.FirewallRuleArgs{
			Direction:      pulumi.String("out"),
			Protocol:       pulumi.String("icmp"),
			DestinationIps: pulumi.StringArray{pulumi.String("0.0.0.0/0"), pulumi.String("::/0")},
		},
	)

	fw, err := hcloud.NewFirewall(ctx, fwName, &hcloud.FirewallArgs{
		Name:   pulumi.String(fwName),
		Labels: toStringMap(tags.HetznerLabels(h.project, env, h.slug, spec.Container, h.eph)),
		Rules:  rules,
	}, h.providerOpts()...)
	if err != nil {
		return nil, fmt.Errorf("create firewall %s: %w", fwName, err)
	}

	h.firewalls[fwName] = fw
	return fw, nil
}

// ensureSshKeys returns the [user, deploy] SSH key pair for env, creating them
// if they do not yet exist. SSH keys are env-scoped (not region-scoped). It is
// safe to call concurrently. Index 0 is the user authorized-keys key; index 1
// is the deploy public key.
func (h *HetznerCompute) ensureSshKeys(ctx *pulumi.Context, env string) ([]*hcloud.SshKey, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if keys, ok := h.sshKeys[env]; ok {
		return keys, nil
	}

	// SSH keys are env-scoped, not container-scoped: omit container label.
	keyLabels := toStringMap(tags.HetznerLabels(h.project, env, h.slug, "", h.eph))

	userKeyName := naming.GlobalResource(env, "key", "user")
	userKey, err := h.newOrImportSshKey(ctx, userKeyName, h.sshAuthorizedKeys, keyLabels)
	if err != nil {
		return nil, fmt.Errorf("create user ssh key %s: %w", userKeyName, err)
	}

	deployKeyName := naming.GlobalResource(env, "key", "deploy")
	deployKey, err := h.newOrImportSshKey(ctx, deployKeyName, h.deployPublicKey, keyLabels)
	if err != nil {
		return nil, fmt.Errorf("create deploy ssh key %s: %w", deployKeyName, err)
	}

	keys := []*hcloud.SshKey{userKey, deployKey}
	h.sshKeys[env] = keys
	return keys, nil
}

// newOrImportSshKey creates an SSH key in Hetzner, importing the existing one
// if a key with the same name is already present (adopt-or-create, idempotent).
// It uses a direct hcloud API call rather than Pulumi's LookupSshKey invoke,
// which logs engine-level errors on 404 and fails the stack even when caught.
func (h *HetznerCompute) newOrImportSshKey(ctx *pulumi.Context, name, publicKey string, labels pulumi.StringMap) (*hcloud.SshKey, error) {
	opts := h.providerOpts()
	if id, err := h.lookupSshKeyID(name); err == nil && id != 0 {
		opts = append(opts, pulumi.Import(pulumi.ID(strconv.Itoa(id))))
	}
	return hcloud.NewSshKey(ctx, name, &hcloud.SshKeyArgs{
		Name:      pulumi.String(name),
		PublicKey: pulumi.String(publicKey),
		Labels:    labels,
	}, opts...)
}

// lookupSshKeyID queries the hcloud API directly for an SSH key by name and
// returns its numeric ID, or 0 if not found. Errors are treated as not-found
// so a missing or misconfigured token falls back to create rather than failing.
func (h *HetznerCompute) lookupSshKeyID(name string) (int, error) {
	if h.apiToken == "" {
		return 0, nil
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://api.hetzner.cloud/v1/ssh_keys?name="+name, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	var result struct {
		SSHKeys []struct {
			ID int `json:"id"`
		} `json:"ssh_keys"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}
	if len(result.SSHKeys) == 0 {
		return 0, nil
	}
	return result.SSHKeys[0].ID, nil
}

// providerOpts returns the Pulumi provider resource option when a provider is
// set, and nil otherwise (used in tests where provider is nil).
func (h *HetznerCompute) providerOpts() []pulumi.ResourceOption {
	if h.provider == nil {
		return nil
	}
	return []pulumi.ResourceOption{pulumi.Provider(h.provider)}
}
