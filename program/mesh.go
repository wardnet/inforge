package program

import (
	"sort"

	"github.com/wardnet/inforge/internal/pki"
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
