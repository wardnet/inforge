// Package meshplan is the single, pure derivation of which mesh-member services
// (those declaring pki:) sit on which canonical compute host. Both the deploy
// program (per-host mesh nginx inputs, egress-port assignment) and the renewal
// path (per-host provider writes of leaf + bundle material) consume it, so the
// set of services a mesh host serves can never drift between the two — the same
// single-source discipline as the egress-port rule
// (.agents/rules/mesh-host-grouping-is-single-sourced.md).
//
// The package is deliberately program-free: it must stay importable from the
// renewal path (cmd/inforge pki renew), which never runs the Pulumi program.
package meshplan

import (
	"sort"

	"github.com/wardnet/inforge/internal/types"
)

// ServicesByHost groups a resource set's mesh-member services (pki: non-empty)
// by their canonical compute host key. Services whose host: does not resolve in
// the canonical map are skipped (validation rejects them long before this
// runs). Within each host the services are sorted by name — the order the
// deterministic egress-port assignment indexes into.
func ServicesByHost(res types.Resources, canonical map[string]string) map[string][]types.ServiceSpec {
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
	for _, svcs := range byHost {
		sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
	}
	return byHost
}

// HostKeys returns the sorted host keys of a ServicesByHost result.
func HostKeys(byHost map[string][]types.ServiceSpec) []string {
	out := make([]string, 0, len(byHost))
	for k := range byHost {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
