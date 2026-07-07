---
status: superseded by ADR-0035
date: 2026-07-03
issue: "#162"
---

# Mesh leaf delivery is pull-based via a per-host mesh identity

> Completes ADR-0032's "leaf custody shifts" with the delivery mechanism: how each
> mesh proxy comes to hold its co-located services' real leaves + trust bundle, at
> deploy, on renewal, and across reboots. Reverses the earlier "full SSH-push"
> working decision.

## Context

ADR-0032 moved east-west cert custody into the per-host mesh proxy: the mesh nginx
presents each co-located service's leaf (server cert on ingress, client cert on
egress) from `meshpaths.RuntimeDir` (tmpfs), and the service holds **no** east-west
cert material. The Pulumi delivery (#161) realized the proxy on **only-when-absent
self-signed placeholders** so `nginx -t` passes before real material exists. This
ADR decides how real material lands and stays fresh.

The working decision recorded before this slice was a **full SSH-push**: `inforge
pki renew` would scp refreshed leaves/bundle to every mesh host and reload nginx.
Designing it surfaced an unsolvable corner: `RuntimeDir` is tmpfs (deliberately —
leaf keys never touch disk, and systemd must not own the dir's lifecycle), so a
reboot wipes real material. With push there is nothing on the host or reachable
*by* the host to restore it — the choices degrade to keys-at-rest on disk, or a
mesh outage until the next renew cron.

## Decision

**Pull, not push.** The mesh proxy is a first-class secrets consumer, exactly like
a service:

- **Provider layout.** Each scope gets one shared **mesh workspace** (container
  name `mesh` → `wardnet-<env>-<slug>-container-mesh`). Each mesh host has its own
  path `/<hostKey>/` holding `<svc>/leaf.crt` + `<svc>/leaf.key` per co-located
  service and one shared `bundle.crt` (the concatenated trust sets of every mesh
  its services belong to). Key scheme single-sourced in `internal/meshpaths`
  (`LeafCertKey`/`LeafKeyKey`/`BundleKey`) so `RuntimeDir + key == material path`
  by construction.
- **Per-host identity.** Deploy (`infisical.ProvisionMeshHost`, wired from
  `realizeMesh` → `deliverMeshHost`) mints a machine identity per mesh host whose
  read scope covers **only** `/<hostKey>` — it can read exactly the material its
  proxy already holds in memory, so a leaked credential broadens nothing. Deploy
  writes a **mesh descriptor** (`agent.MeshDescriptor`, v1, strict fields) plus a
  host-key-encrypted `credential.age` to `meshpaths.AgentDir`
  (`/etc/wardnet/mesh`).
- **The pull.** `inforge-agent mesh-project <dir>` fetches the host's path and
  projects it into the tmpfs `RuntimeDir` (owner nginx, 0400, two-pass atomic set
  per `atomic-pem-projection`). It runs (a) as the mesh unit's **first
  ExecStartPre** — so a reboot re-seeds **real** material before nginx starts, the
  tmpfs custody keeps keys off disk, and the placeholder seed (which stays, and
  runs second) only ever fills first-boot gaps — and (b) from a daily
  `wardnet-mesh-renew.timer`, reloading the proxy only on a real change. The pull
  never partially applies (atomic set) and fails HARD on any error — the proxy's
  start is protected by the `-` ExecStartPre prefix instead (a degraded pull
  keeps whatever is on disk and nginx still starts), while the un-prefixed renew
  oneshot surfaces persistent pull breakage as a failed systemd unit, the
  monitorable signal that prevents a silent 90-day drift to leaf expiry.
- **Renewal stays a pure provider write.** `inforge pki renew` mints per
  (service, scope) and writes the per-host aggregates (grouped by the same
  `internal/meshplan` derivation the deploy realizes proxies from) — no SSH, no
  Pulumi, no deploy-key custody in the cron. Hosts converge on their own timer,
  the same contract services already had. Failures accumulate per host/service
  rather than aborting the run: one missing workspace must not starve every
  other consumer of fresh leaves.
- **Deploy baseline.** `inforge deploy` (and `ephemeral up`) runs an imperative
  **post-up** step (`meshBaseline`): mint + provider-write via the renew core,
  then SSH each mesh host (targets from the new `meshDeployDescriptor` stack
  output) and `systemctl start wardnet-mesh-renew.service`. The trigger pushes a
  **signal, never material** — it just makes proxies converge now instead of on
  the next timer tick, so a failed trigger is a WARNING, never a deploy failure
  (only the mint phase, and the up-front SSH-key resolution before it, can fail
  the command). Never inside `program.Run` (no PKI store or leaf keys in Pulumi
  state).
- **The subtractive half is opt-in-gated, not unconditional.** wardnet-cloud
  ADR-0014 keeps `MTLS_*_PATH` for **tunneller only** (its raw node↔node forward
  plane is direct mTLS outside the mesh). A new service manifest boolean
  **`mtls_files: true`** preserves today's service-side path for such services:
  `/<svc>/mtls` provider writes, descriptor `files:`, per-service renew timer, and
  the release-time mint. Every service without it (the default) holds no cert
  material: no `files:`, no `/<svc>/mtls` write, no per-service renew timer, and
  `pki:` alone no longer forces workspace/identity provisioning.

## Consequences

- Reboot self-heals with real material; keys stay tmpfs-only; renewal keeps its
  "renew only writes the provider" philosophy and needs only `INFORGE_SECRETS_KEY`.
- Deploy needs an SSH deploy key (`--ssh-key` / `INFORGE_DEPLOY_KEY`) for the
  baseline trigger when the env has mesh hosts; a failed trigger degrades to
  timer-paced convergence, not a broken deploy target.
- Two new single-source seams: `internal/meshplan` (host grouping shared by
  program + renew — rule `mesh-host-grouping-is-single-sourced`) and the
  `meshpaths` provider-key scheme.
- An mtls_files service's own leaf and the mesh proxy's leaf for it are minted
  independently (two keypairs, same intermediate) — the raw plane and the mesh
  plane never share a private key.
