---
status: accepted
date: 2026-07-06
issue: "#171"
---

# Git-backed, per-host secrets delivery (retiring Infisical)

*Retires Infisical as inforge's runtime secrets backend (superseding the provider-fetch half of
ADR-0010); reverses ADR-0033's pull decision for mesh leaf delivery now that its root cause
(tmpfs-only persistence) no longer holds; extends ADR-0017's git-committed age store and
ADR-0025's grants all the way to the on-host boundary.*

## Context

A ground-up `wardnet-infrastructure/prd` deploy hit Infisical's SaaS plan wall: `Failed to create
workspace due to plan limit reached` on the 4th project. inforge creates one Infisical project per
`(container, environment)` (`InfisicalSecretsAdapter.ensureWorkspace`), so any real topology needs
far more than the free tier's 3 projects.

Self-hosting Infisical (Docker + Postgres + Redis on a ~$7/mo Hetzner VM) was considered and
rejected:

- Its Postgres becomes a **central honeypot** — every service's secret value, for every
  environment, decryptable in one place. This is a strictly worse blast-radius shape than what
  inforge already does elsewhere (per-service Postgres roles via grants, per-host mesh identities).
- It adds a VM + database that must be operated, backed up, and kept available — infrastructure
  inforge doesn't otherwise need to run for itself.
- It introduces a **boot-time network dependency**: a service can't start without reaching the
  vault, even though inforge is fundamentally a git-ops, deploy-driven tool.

**Decision: eliminate the runtime secrets backend entirely.** inforge already has a git-committed,
age-encrypted secret store (`internal/secretstore`, ADR-0017) as its source of truth, and already
age-encrypts material to each host's own SSH host key for delivery (`internal/agehost`). This ADR
extends that existing model all the way to the runtime boundary: secret **values** (not just a
provider login credential) are resolved at deploy/renew time, age-encrypted directly to the
target host's SSH key, and delivered over the same SSH transport inforge already uses. There is no
backend, no central store of decryptable values, no plan wall, and boot no longer depends on a
reachable vault.

## Decision

**Custody model collapses to one persistent artifact.** Today, persistent disk holds only
`credential.age` (a login credential); the actual secret values never touch disk — env-var
secrets go straight into the exec'd child's environment in memory, and only *file* secrets (mesh
leaves, mtls PEMs) are staged into tmpfs after being fetched. Removing the "log in and fetch" step
removes the credential concept entirely: **`secrets.age`, persistent, replaces `credential.age`**
and directly contains the resolved plaintext (both value fields and file fields, mirroring today's
`Descriptor.Env`/`Files` maps), age-encrypted to the host's own SSH key. At boot the agent decrypts
it in memory — value fields go straight into the exec environment exactly as today (never touching
disk at all), file fields still stage into tmpfs `RuntimeDir` via the existing `projectFiles`
atomic-write path, unchanged. The tmpfs-never-persistent invariant for file secrets is preserved;
only the "online login" step is removed.

**Granularity mirrors today's blast-radius scoping, unchanged.** A service gets its own persistent
`secrets.age` (`/etc/wardnet/services/<svc>/secrets.age`) holding only its own resolved
env/grant/mtls values — a compromised service never exposes a sibling's secrets. The mesh proxy
keeps one per-host `secrets.age` (`/etc/wardnet/mesh/secrets.age`) aggregating all co-located
services' leaves + trust bundle, because one shared proxy process needs all of it — exactly the
scoping ADR-0033 already established for the mesh identity, just with a different artifact inside.

**Delivery is push, gated by a plaintext hash, not ciphertext.** Age ciphertext is non-deterministic
(fresh ephemeral key per encryption), so diffing ciphertext would make `secrets.age` look changed on
every deploy even when no value moved. Instead: the program resolves each consumer's final
plaintext env/file map inside a Pulumi `ApplyT` (as it already does via `resolveRef`/
`resolveDatabaseGrants`), hashes the canonical (stable-sorted) plaintext with SHA-256, and uses
**that hash — not the ciphertext — as the `remote.Command`'s `Triggers` input.** Pulumi's own
diff-on-Triggers mechanism becomes the change detector for free: when the hash is unchanged, the
command that writes `secrets.age` doesn't run at all.

**Restart-on-change rides the same trigger.** The reload-or-restart step (reload if the service
declares `reload:`, else restart — the existing pattern) lives inside the *same* `remote.Command`,
so it only executes when Pulumi actually re-runs the command, i.e. only when the plaintext hash
changed. This is a correctness improvement over today, where a secret rotation only takes effect on
the next incidental restart.

**Mesh leaf renewal reverses ADR-0033: push, not pull.** ADR-0033 chose pull specifically because
leaf material lived only in tmpfs — a reboot before the next scheduled pull left the proxy with
nothing to restore from. That root cause is gone: the encrypted source is now persistent
(`secrets.age`), so a reboot re-decrypts fresh material from disk regardless of when the last
renewal ran. `inforge pki renew` — which already resolves per-host SSH targets and keys for its
existing baseline-trigger step — instead SSH-pushes the updated `secrets.age` directly, then
unconditionally signals reload-or-restart (renewal always mints a fresh leaf, so it always differs;
no hash-gating is needed on this path, matching the daily pull model's implicit always-reload
behavior today). This removes both on-host renewal timers (`wardnet-<svc>-renew.timer`,
`wardnet-mesh-renew.timer`) and both pull subcommands (`inforge-agent project`, `mesh-project`) —
nothing is left to poll or retry against. The mesh proxy's `ExecStartPre` self-heal pull also goes
away: since `secrets.age` is persistent, a reboot's ordinary boot flow (decrypt → project → start)
*is* the self-heal, with no special pre-step. The placeholder self-signed-cert seed is unrelated to
custody and is unchanged (first-boot/corruption fallback so `nginx -t` passes before real material
lands).

**Grants are unaffected upstream of the sink.** `resolveDatabaseGrants` still mints scoped Postgres
roles and produces `grantSecrets`; those values simply merge into the new plaintext map instead of
an Infisical batch. No change to role minting or `interpolateGrantOutput`.

**The provider abstraction is removed, not repointed.** `types.ServiceSecretsProvisioner`,
`types.ServiceSecretsBundle`, and `registry.ServiceSecretsProvisioner`/`SecretsProviderName()` are
deleted outright rather than gaining a second implementation. This isn't a pluggable third-party
integration users choose between (unlike `hetzner`/`cloudflare`/`neon`) — it becomes a fixed,
intrinsic part of `program.go`/`mesh.go`, the same way `internal/agehost` and `internal/secretstore`
already are. There is consequently **no provider config surface** to replace
`infisical.clientId/siteUrl/organizationId/...` with — nothing takes its place in `regions.yaml`.

**Descriptor versions bump for a removed field.** `agent.Provider{Kind,URL,Project,Environment,
SecretPath}` is entirely Infisical-shaped (workspace/environment-slug/REST-URL) and disappears from
both descriptors. `Descriptor.SupportedVersion` bumps 6→7; `MeshDescriptor`'s bumps 1→2 (both are
strict `KnownFields` decoders — removing a field is exactly the kind of change the existing
versioning discipline already treats as breaking). `Descriptor.Env`/`.Files` are **retained** as
non-secret structure (which env var or file a value belongs to); only where the *values* come from
changes, from "fetch key under a workspace path" to "key inside the decrypted `secrets.age` map."

**Full removal, not deprecation.** `providers/infisical/*` (adapter, `CertWriter`, the three Pulumi
custom resources, the REST client) is deleted, along with its `pulumi-resource-infisical`
goreleaser target and README binaries-table row. `internal/agent/infisical.go` is deleted; since
there is no remote call left to retry, `SecretsFetcher`/`FetchWithBackoff` and the transient-error
retry harness in `fetch.go` are deleted too, replaced by a plain local decrypt-and-parse.

## Consequences

**Gains:**

- No central honeypot — a compromised host only ever exposes its own secrets, encrypted to its own
  key; strictly smaller blast radius than a shared vault.
- No VM/database to operate, back up, or keep available; no SaaS plan wall.
- Boot no longer depends on a reachable external service.
- Secret rotation via a plain `inforge deploy` now actually restarts/reloads the affected service —
  a correctness improvement over today's silent no-op.
- Fewer moving parts on-host: no renewal timers, no pull/retry machinery, no provider REST client
  in the agent binary.

**Losses (accepted):**

- No backend-side runtime secret rotation without a deploy or renew — acceptable, since inforge is
  deploy-driven; a secret change is a merge + deploy/renew anyway.
- No central read-audit log — acceptable for a self-hosted, single-tenant deployment.
- `inforge pki renew` now requires outbound SSH reachability to every mesh/mtls host at renewal
  time (today's pull model only needed the host to reach the provider). This is not new capability
  — `meshBaseline`'s existing post-deploy trigger step already SSHes to every mesh host — but it
  does mean renewal's reliability now depends on SSH reachability rather than on the provider being
  up, a real trade-off worth naming rather than one to relitigate.
- Compiled-in age-encryption keys and the git-committed store remain the single source of truth;
  losing `INFORGE_SECRETS_KEY` still means losing the ability to rotate secrets (unchanged from
  today — not a new risk introduced by this ADR).

This does not change `inforge pki renew`'s standing invariant that it never runs Pulumi
(`.agents/rules/pki-renew-never-runs-pulumi.md`) — the new SSH push is an imperative step alongside
the existing imperative leaf-minting, exactly like the deploy-time SSH trigger `meshBaseline` already
performs.
