package program

import (
	"fmt"
	"sort"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/agent"
	"github.com/wardnet/inforge/internal/meshnginx"
	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/meshplan"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/registry"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// meshGatewayCaller is the reserved mesh.allowed_services token naming the north-south
// gateway's mesh identity (<scope>/gateway) — the same token validate enforces.
const meshGatewayCaller = "gateway"

// expandAllowedCallers turns a callee service's bare mesh.allowed_services names into the
// exact set of caller mesh identities (<scope>/<service>) its local mesh ingress admits,
// per the ADR-0032 direction rules. This is the security-critical projection of the
// authored allow-list onto the concrete identities peers present — over-admitting is a
// hole, under-admitting breaks calls.
//
// Rules, by the callee's scope:
//   - "gateway" ⇒ <calleeScope>/gateway. The gateway routes only same-scope services, so a
//     callee is only ever called by its OWN scope's gateway.
//   - regional callee (calleeScope is a region): a caller "svc" ⇒ <calleeScope>/svc.
//     Callers are same-region only — a global service may not call a regional one, and
//     cross-region is segregated. Validation guarantees the name is a same-scope mesh member.
//   - global callee: a caller "svc" that is a REGIONAL mesh service ⇒ <r>/svc for EVERY
//     deploying region r (regional→global is permitted from any region); a caller that is a
//     GLOBAL mesh service ⇒ global/svc. A name that is both expands to both.
//
// calleeScope is the callee's scope (a region name or pki.ScopeGlobal). regions is every
// deploying region name. regionalMesh / globalMesh are the sets of mesh-member service
// names in the regional and global resource sets. The result is sorted and de-duplicated.
func expandAllowedCallers(allowed []string, calleeScope string, regions []string, regionalMesh, globalMesh map[string]bool) []string {
	set := map[string]bool{}
	for _, name := range allowed {
		switch {
		case name == meshGatewayCaller:
			set[calleeScope+"/"+meshGatewayCaller] = true
		case calleeScope != pki.ScopeGlobal:
			// Regional callee — same-region caller only.
			set[calleeScope+"/"+name] = true
		default:
			// Global callee — a regional caller may reach it from any region; a global
			// caller is global-scoped. Guarded by the mesh-membership sets so a name that
			// is one but not the other expands only where it exists.
			if regionalMesh[name] {
				for _, r := range regions {
					set[r+"/"+name] = true
				}
			}
			if globalMesh[name] {
				set[pki.ScopeGlobal+"/"+name] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// meshHostInputs is a host's local (callee) and egress (caller) mesh renderer inputs,
// fully resolved except for the routing-table addresses (which come from compute IPs and
// are filled by the deploy pass, not here).
type meshHostInputs struct {
	local  []meshnginx.LocalService
	egress []meshnginx.EgressCaller
}

// meshInputsByHost groups a scope's mesh services (those declaring pki:) by their
// canonical host into the per-host local + egress renderer inputs, via the shared
// meshplan grouping (the renewal path writes per-host material over the same
// grouping — see .agents/rules/mesh-host-grouping-is-single-sourced.md). A pki
// service is ALWAYS an egress caller (it can make outbound mesh calls); one that
// also declares a mesh: block is additionally a local callee (it receives). Egress
// ports are assigned deterministically per host — meshpaths.EgressPort(index) over
// the host's services in sorted-name order — so the deploy side and the injected
// INFORGE_MESH_URL agree. Leaf paths are the mesh proxy's on-host material paths
// (the custody shift). allowedFor expands a callee's authored allow-list to caller
// identities (see expandAllowedCallers).
func meshInputsByHost(res types.Resources, canonical map[string]string, scope string, allowedFor func(types.ServiceSpec) []string) map[string]*meshHostInputs {
	byHost := meshplan.ServicesByHost(res, canonical)
	out := make(map[string]*meshHostInputs, len(byHost))
	for host, svcs := range byHost {
		mh := &meshHostInputs{}
		out[host] = mh
		for i, svc := range svcs {
			mh.egress = append(mh.egress, meshnginx.EgressCaller{
				Name:         svc.Name,
				EgressPort:   meshpaths.EgressPort(i),
				LeafCertPath: meshpaths.LeafCertPath(svc.Name),
				LeafKeyPath:  meshpaths.LeafKeyPath(svc.Name),
			})
			if svc.Mesh != nil {
				// The callee's declared endpoint surface (ADR-0034): peers may reach
				// public AND internal paths — visibility only splits at the gateway.
				paths := append([]string(nil), svc.Mesh.PublicPaths...)
				paths = append(paths, svc.Mesh.InternalPaths...)
				mh.local = append(mh.local, meshnginx.LocalService{
					Name:           svc.Name,
					SNI:            meshpaths.DNSName(scope, svc.Name),
					MeshPort:       svc.Mesh.Port,
					LeafCertPath:   meshpaths.LeafCertPath(svc.Name),
					LeafKeyPath:    meshpaths.LeafKeyPath(svc.Name),
					AllowedCallers: allowedFor(svc),
					Paths:          paths,
				})
			}
		}
	}
	// The north-south gateway is an extra egress caller on its host (ADR-0032: a
	// mesh client, never a callee) — appended AFTER the positional services with
	// its FIXED reserved port, so the sorted-index port math above (and the
	// INFORGE_MESH_URL injection that mirrors it) is untouched. A gateway-only
	// host gets an egress-only mesh proxy.
	for host, gw := range meshplan.GatewayMemberByHost(res, canonical) {
		mh := out[host]
		if mh == nil {
			mh = &meshHostInputs{}
			out[host] = mh
		}
		mh.egress = append(mh.egress, meshnginx.EgressCaller{
			Name:         gw.Name,
			EgressPort:   meshpaths.GatewayEgressPort,
			LeafCertPath: meshpaths.LeafCertPath(gw.Name),
			LeafKeyPath:  meshpaths.LeafKeyPath(gw.Name),
		})
	}
	return out
}

// meshEgressPortsByService maps each pki: service name in a scope to its
// deterministic loopback egress port — the port its INFORGE_MESH_URL points at.
// It MUST agree byte-for-byte with meshInputsByHost's per-host assignment (the
// mesh proxy binds those egress listeners), so both consume the same shared
// meshplan grouping: per canonical host, sorted by service name,
// meshpaths.EgressPort(index). The descriptor render path calls this to inject
// INFORGE_MESH_URL; the realization binds the matching listener, so the
// caller's URL and the mesh port never drift.
func meshEgressPortsByService(res types.Resources, canonical map[string]string) map[string]int {
	out := map[string]int{}
	for _, svcs := range meshplan.ServicesByHost(res, canonical) {
		for i, svc := range svcs {
			out[svc.Name] = meshpaths.EgressPort(i)
		}
	}
	return out
}

// meshTarget is one routing-table entry before IP resolution: the target callee
// service, its mesh SNI, the canonical compute key of the host serving it, and
// whether that host is resolved in the GLOBAL compute set via its PUBLIC IP
// (a cross-scope target a regional mesh dials over the internet) vs the scope's
// own compute set via its PRIVATE IP (a same-scope target).
type meshTarget struct {
	name   string
	sni    string
	host   string
	global bool
}

// meshTargets builds a scope's routing table — host-global, identical for every
// mesh host in the scope (ADR-0032): a target is reached at its host's address,
// independent of which host asks. It admits every callee (a service with a mesh:
// block — a pki: service with no mesh: block exposes nothing inbound, so it is
// never a target) in the scope, reached at that host's PRIVATE IP; and — for a
// regional scope only — every GLOBAL callee, reached at its global host's PUBLIC
// IP (the cross-scope hop). A name defined in both scopes resolves same-scope
// (added first; the global entry is skipped), so a regional caller reaches its
// own region's service. The result is sorted by target name.
func meshTargets(scopeRes, globalRes types.Resources, scopeCanonical, globalCanonical map[string]string, meshScope string, isGlobal bool) []meshTarget {
	var out []meshTarget
	seen := map[string]bool{}
	for _, svc := range scopeRes.Service {
		if svc.Mesh == nil {
			continue
		}
		host, ok := scopeCanonical[svc.Host]
		if !ok || seen[svc.Name] {
			continue
		}
		seen[svc.Name] = true
		out = append(out, meshTarget{name: svc.Name, sni: meshpaths.DNSName(meshScope, svc.Name), host: host})
	}
	if !isGlobal {
		for _, svc := range globalRes.Service {
			if svc.Mesh == nil {
				continue
			}
			host, ok := globalCanonical[svc.Host]
			if !ok || seen[svc.Name] {
				continue
			}
			seen[svc.Name] = true
			out = append(out, meshTarget{name: svc.Name, sni: meshpaths.DNSName(pki.ScopeGlobal, svc.Name), host: host, global: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// meshMembers is the set of pki: (mesh-member) service names in a resource set —
// the caller-identity universe expandAllowedCallers projects an allow-list onto.
func meshMembers(res types.Resources) map[string]bool {
	out := map[string]bool{}
	for _, svc := range res.Service {
		if svc.Pki != "" {
			out[svc.Name] = true
		}
	}
	return out
}

// realizeMesh installs the east-west mesh proxy (the second, private nginx) on
// every host in a scope that runs ≥1 pki: service (ADR-0032). It mirrors
// realizeIngress: it derives the per-host renderer inputs (meshInputsByHost) and
// the scope-wide routing table (meshTargets), then per host resolves the listen
// address and each target's Addr from compute IPs inside the provider's apply and
// installs + configures the mesh nginx.
//
// scopeRes is the scope's resource set; globalRes is the global slice (for the
// cross-scope targets a regional mesh reaches over the internet). computeOut is
// the full region-keyed compute output map — the scope's own outputs supply
// same-scope private IPs and this host's listen address, and computeOut[global]
// supplies a cross-scope target's public IP. scopeKey is the scope's output-map
// key (a region name or globalScope); regionNames is every deploying region (for
// the global-callee allow-list expansion).
func realizeMesh(ctx *pulumi.Context, reg registry.ProviderRegistry, scopeRes, globalRes types.Resources, computeOut map[string]map[string]types.ComputeOutputs, scopeKey, slug string, regionNames []string, gates map[string]pulumi.Resource, deployPrivateKey, env string, defaults types.ProviderDefaults) error {
	isGlobal := scopeKey == globalScope
	// The mesh scope is the region name, or the literal ScopeGlobal for the global
	// slice — the segment in the leaf SNI and the caller identity.
	meshScope := scopeKey

	scopeCanonical := naming.CanonicalComputeKeys(scopeRes.Compute)
	globalCanonical := naming.CanonicalComputeKeys(globalRes.Compute)
	regionalMesh := meshMembers(scopeRes)
	globalMesh := meshMembers(globalRes)

	allowedFor := func(svc types.ServiceSpec) []string {
		return expandAllowedCallers(svc.Mesh.AllowedServices, meshScope, regionNames, regionalMesh, globalMesh)
	}
	inputsByHost := meshInputsByHost(scopeRes, scopeCanonical, meshScope, allowedFor)
	if len(inputsByHost) == 0 {
		return nil
	}

	// The scope-wide routing table, plus the per-target IP output each Addr resolves
	// against (private same-scope, public cross-scope). Built once — every host in
	// the scope shares the same table.
	targets := meshTargets(scopeRes, globalRes, scopeCanonical, globalCanonical, meshScope, isGlobal)
	cfgTargets := make([]meshnginx.Target, 0, len(targets))
	targetIPs := map[string]pulumi.StringOutput{}
	scopeCompute := computeOut[scopeKey]
	globalCompute := computeOut[globalScope]
	for _, t := range targets {
		if t.global {
			gc, ok := globalCompute[t.host]
			if !ok {
				return fmt.Errorf("mesh: global target %q host %q has no compute output", t.name, t.host)
			}
			targetIPs[t.name] = gc.PublicIP
		} else {
			sc, ok := scopeCompute[t.host]
			if !ok {
				return fmt.Errorf("mesh: target %q host %q has no compute output", t.name, t.host)
			}
			targetIPs[t.name] = sc.PrivateIP
		}
		cfgTargets = append(cfgTargets, meshnginx.Target{Name: t.name, SNI: t.sni})
	}

	deployUserByCompute := naming.DeployUsersByHost(scopeRes.Compute)
	providerByCompute := ingressProvidersByHost(scopeRes.Compute, defaults)

	for _, hostKey := range sortedKeys(inputsByHost) {
		mh := inputsByHost[hostKey]
		host, ok := scopeCompute[hostKey]
		if !ok {
			return fmt.Errorf("mesh: host %q has no compute output (available: %v)", hostKey, sortedKeys(scopeCompute))
		}
		mp, err := reg.Mesh(providerByCompute[hostKey])
		if err != nil {
			return fmt.Errorf("mesh: host %q: %w", hostKey, err)
		}
		gate, err := cloudInitGate(ctx, gates, hostKey, host, deployPrivateKey, env, slug)
		if err != nil {
			return err
		}
		cfg := meshnginx.Config{
			TrustBundlePath: meshpaths.BundlePath,
			Local:           mh.local,
			Egress:          mh.egress,
			Targets:         cfgTargets,
		}
		// A regional mesh binds the host's private IP; the global mesh binds 0.0.0.0
		// (it additionally accepts cross-scope peers publicly — the mesh gateway).
		var listenAddr pulumi.StringInput = host.PrivateIP
		if isGlobal {
			listenAddr = pulumi.String("0.0.0.0")
		}
		if err := mp.Realize(ctx, hostKey, host, deployUserByCompute[hostKey], cfg, listenAddr, targetIPs, env, []pulumi.Resource{gate}); err != nil {
			return err
		}
		// The on-host mesh descriptor (secret-free): the service list is the
		// egress set — every co-located pki: service, already sorted. There is no
		// material to deliver here (ADR-0035): the mesh proxy's leaf.age is
		// written later, directly, by `inforge pki renew`'s SSH push.
		services := make([]string, 0, len(mh.egress))
		for _, e := range mh.egress {
			services = append(services, e.Name)
		}
		if err := deliverMeshHost(ctx, hostKey, host, services, deployUserByCompute[hostKey], deployPrivateKey, env, slug, gate); err != nil {
			return err
		}
	}
	return nil
}

// deliverMeshHost writes the on-host mesh descriptor (0644, secret-free —
// just the co-located Services list) into meshpaths.AgentDir. Unlike a
// service, a mesh host gets no secrets.age/leaf.age from deploy: the mesh
// proxy has never received deploy-time secrets (ProvisionMeshHost only ever
// minted pull-access identity, never material), and under ADR-0035 the actual
// leaf + trust-bundle content is delivered later, directly, by `inforge pki
// renew`'s SSH push as leaf.age — a separate persistent artifact this deploy
// pass does not write (see docs/adr/0035, refinement 1). So there is no host
// key to read and nothing to encrypt here — a single static command suffices.
func deliverMeshHost(ctx *pulumi.Context, hostKey string, host types.ComputeOutputs, services []string, deployUser, deployPrivateKey, env, slug string, gate pulumi.Resource) error {
	conn := iremote.Connection(host.PublicIP, deployUser, deployPrivateKey)
	name := naming.Resource(env, slug, "mesh", hostKey)

	descriptor, err := renderMeshDescriptor(services)
	if err != nil {
		return fmt.Errorf("mesh host %q: %w", hostKey, err)
	}

	// A Triggers change (the service list changed) replaces this resource;
	// writeHostFile's delete-before-replace keeps the old Delete script from
	// removing the freshly written descriptor after the new Create (see
	// deliverServiceSecrets).
	if _, err := writeHostFile(ctx, name, "-agent", conn, meshpaths.DescriptorPath, descriptor, gate); err != nil {
		return fmt.Errorf("mesh host %q: write mesh descriptor: %w", hostKey, err)
	}
	return nil
}

// renderMeshDescriptor marshals the on-host mesh descriptor, building the
// agent's own MeshDescriptor struct (imported, not duplicated) so the producer
// can never drift from the consumer's schema.
func renderMeshDescriptor(services []string) (string, error) {
	d := agent.MeshDescriptor{
		Version:  agent.MeshSupportedVersion,
		Services: services,
	}
	b, err := yaml.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("marshal mesh descriptor: %w", err)
	}
	return string(b), nil
}
