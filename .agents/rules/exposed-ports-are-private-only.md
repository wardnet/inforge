# exposed_ports open only to the private network — never publicSources

A service's `exposed_ports` (`[]types.ExposedPort{Proto, Port}`, ADR-0029) are ports inforge opens on
the host's **private-network CIDR only**. They must **never** be added to the public source set
(`0.0.0.0/0` + `::/0`). A port that must be reachable from the internet is a `compute.firewall.inbound`
rule — that is the public primitive; `exposed_ports` is its private sibling. The two must not converge.

## The invariant

- The firewall plan carries exposed ports in `FirewallPorts.PrivateExposed` (separate from `Public`),
  and the provider scopes them to `FirewallPorts.PrivateSourceCIDR` (the host's network CIDR) — the
  exact path that scopes a cross-host route `target` to the private network. In Hetzner's
  `ensureFirewall` the private branch is guarded by `if privateSources != nil`; exposed ports are added
  there via `addRule(ep.Proto, …, privateSources, "private")`, never via the `publicSources` callers.
- `PrivateSourceCIDR` must be set whenever `PrivateExposed` is non-empty (the guard in
  `firewallPlanByHost` is `len(fp.Private) > 0 || len(fp.PrivateExposed) > 0`). Without a known CIDR the
  provider drops the rules rather than silently opening them to the internet.
- exposed ports are read from **every** service directly in `firewallPlanByHost`, not via
  `resolveIngressServices` (which skips ingress-less services) — a private-only service with no ingress
  must still contribute its rules.

## Realization is provider-agnostic

The concept is modelled provider-agnostically (`FirewallPorts.PrivateExposed`). Only Hetzner exists
today; another provider whose private network needs no per-port rule may no-op — but no provider may
render an exposed port as a public rule.

## Applies to

`internal/types/types.go` (`ExposedPort`, `ServiceSpec.ExposedPorts`, `FirewallPorts.PrivateExposed`);
`program/program.go` (`firewallPlanByHost`, `sortedExposedPorts`); `providers/hetzner/compute.go`
(`ensureFirewall`, `addRule`); `internal/validate/validate.go` (`checkService`,
`udpExposedUsersByHost`); `schemas/service.json`.

## Why

The whole point of `exposed_ports` is the one exposure mode the existing three (ingress routes,
`firewall.inbound`, the health tier) could not express: private-network-only. Leaking even one exposed
port to `0.0.0.0/0` would defeat the feature and silently expose peer/service-to-service traffic (e.g. a
`tunneller`'s inter-node mesh-mTLS listener) to the internet.
