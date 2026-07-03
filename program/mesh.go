package program

import (
	"sort"

	"github.com/wardnet/inforge/internal/meshnginx"
	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/types"
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
// canonical host into the per-host local + egress renderer inputs. A pki service is
// ALWAYS an egress caller (it can make outbound mesh calls); one that also declares a
// mesh: block is additionally a local callee (it receives). Egress ports are assigned
// deterministically per host — meshpaths.EgressPort(index) over the host's services in
// sorted-name order — so the deploy side and the injected INFORGE_MESH_URL agree. Leaf
// paths are the mesh proxy's on-host material paths (the custody shift). allowedFor
// expands a callee's authored allow-list to caller identities (see expandAllowedCallers).
func meshInputsByHost(res types.Resources, canonical map[string]string, scope string, allowedFor func(types.ServiceSpec) []string) map[string]*meshHostInputs {
	byHost := map[string][]types.ServiceSpec{}
	for _, svc := range res.Service {
		if svc.Pki == "" {
			continue
		}
		host, ok := canonical[svc.Host]
		if !ok {
			continue
		}
		byHost[host] = append(byHost[host], svc)
	}
	out := make(map[string]*meshHostInputs, len(byHost))
	for host, svcs := range byHost {
		sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
		mh := &meshHostInputs{}
		for i, svc := range svcs {
			mh.egress = append(mh.egress, meshnginx.EgressCaller{
				Name:         svc.Name,
				EgressPort:   meshpaths.EgressPort(i),
				LeafCertPath: meshpaths.LeafCertPath(svc.Name),
				LeafKeyPath:  meshpaths.LeafKeyPath(svc.Name),
			})
			if svc.Mesh != nil {
				mh.local = append(mh.local, meshnginx.LocalService{
					Name:           svc.Name,
					SNI:            meshpaths.DNSName(scope, svc.Name),
					MeshPort:       svc.Mesh.Port,
					LeafCertPath:   meshpaths.LeafCertPath(svc.Name),
					LeafKeyPath:    meshpaths.LeafKeyPath(svc.Name),
					AllowedCallers: allowedFor(svc),
				})
			}
		}
		out[host] = mh
	}
	return out
}
