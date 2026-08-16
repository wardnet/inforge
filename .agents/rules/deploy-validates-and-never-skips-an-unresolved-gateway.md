# `deploy` validates the tree, and an unresolved gateway FK fails it

Two coupled invariants, both learned from one production outage.

## 1. `inforge deploy` validates before it touches state

`runDeploy` runs `validate.ValidateResources` on the tree it is about to apply, **before**
the stack lock check, the stack upsert, and the engine run. Do not remove it, do not make
it a flag, and do not assume "CI validated the PR" is equivalent — nothing enforces that
the tree a deploy applies is the tree that passed a check. It is a pure static pass (no
credentials, no network), so it costs a deploy nothing but the read.

For an ephemeral stack, validate the **source** environment — the tree that will actually
be read (see `ephemeral-identity-vs-config-source`), not the slug identity.

## 2. A gateway whose FKs do not resolve fails the deploy

Every gateway derivation reaches the gateway through `program.resolveGateways`, which
**skips** an entry whose `host:` or `ingress:` FK does not resolve. That skip is correct
locally — it keeps each derivation total — but it is applied by four consumers at once
(`firewallPlanByHost`, `gatewayEdgeByHost`, `resolveGatewayHealthServices`,
`derivedRecords`), so an unresolvable gateway does not degrade: it disappears completely,
and nginx renders, tests green, with no north-south edge at all.

`program.checkGatewayFKs` runs at the top of `createInfra` and turns that into a hard
error. Keep the per-derivation skips (they stay total); keep the check that makes them
unreachable. If you add a fifth gateway derivation, it inherits the guarantee — do not
add a fifth place that decides what to do about an unresolved gateway.

## Applies to

`cmd/inforge/deploy.go` (`runDeploy`), `program/program.go` (`checkGatewayFKs`,
`createInfra`, `resolveGateways` and its consumers), `program/gateway_fk_test.go`.

## Why

ADR-0045 made `gateway.ingress` a required FK. The deployed manifests never declared it.
`deploy` did not validate, so the requirement was never enforced where it mattered; the
PR check that *did* catch it went red and was merged past. The next deploy resolved both
gateways to nothing, reloaded both ingress hosts' nginx without a gateway server, and
reported a successful config apply while the entire public API — every north-south route
in both scopes — went dark.

A skip is the right answer to "this derivation cannot see the resource". It is the wrong
answer to "the deploy cannot see the resource". Fail loudly; a missing edge is never a
partial success.
