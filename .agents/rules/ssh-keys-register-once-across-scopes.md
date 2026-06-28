# Env-scoped SSH keys must register once across all scopes

`program.Run` builds one `registry.BuildRegistry` — and therefore one
`hetzner.HetznerCompute` — per realization scope (the region-less global slice plus one per
region), all sharing the same Pulumi `ctx`. Hetzner SSH keys are **account-global** and named with no
region slug (`wardnet-<env>-key-user` / `wardnet-<env>-key-deploy`, via `naming.GlobalResource`). If
each scope's compute provider registers them independently, Pulumi fails the whole preview/up with
`Duplicate resource URN '…hcloud:index/sshKey:SshKey::wardnet-<env>-key-user'`.

This is the sibling of `registry-provider-names-are-region-scoped.md`. There the fix was to make each
provider URN unique per scope (every scope legitimately needs its own provider). Here it is the
opposite: the SSH keys are a single env-scoped resource pair that must be created **exactly once
total**, not once per scope.

So the SSH key cache must be **shared across every `HetznerCompute` of one program run**:
`program.Run` creates one `registry.NewSSHKeyCache()` and threads it through both `BuildRegistry`
calls → `registry.Compute` → `hetzner.NewCompute`. `ensureSshKeys` dedups against that shared
`*hetzner.SSHKeyCache`, so the first scope to provision a server registers the keys and every other
scope reuses the same key objects.

### Owning provider must be scope-order-independent

Which scope wins the dedup race depends on realization order (global slice first when it provisions a
server, else the sorted-first region) — so the keys must **not** be registered under that winning
scope's region-scoped provider (`hcloud-<region>`). If they were, adding a region that sorts earlier,
removing the first region, or adding a compute-bearing global slice later would move the keys' provider
reference between runs, and Pulumi can replace an account-global resource every server depends on when
its provider changes. Instead `ensureSshKeys` registers them under a **dedicated, fixed-name** provider
(`hcloud-ssh-keys`, `SSHKeyCache.provider`), created once on the cache. Because the cache already
dedups key creation to a single caller, this provider is registered exactly once and never collides on
URN the way a per-scope `hcloud-<region>` provider would — it is the deliberate exception to
`registry-provider-names-are-region-scoped.md`, valid **only** because it is account-global and
single-registration.

This assumes **one Hetzner account per env**: the keys' env-scoped names carry no account dimension,
and the dedicated provider is created with a single token (the winning scope's). `regions.yaml` can
technically set a different `hetzner.apiToken` per region, but that is unsupported — the key would be
created in only one account and other-account servers would fail to reference it by name.

## Applies to

- `providers/hetzner/compute.go` — `SSHKeyCache` (incl. its dedicated `provider`), `NewSSHKeyCache`,
  `HetznerCompute.sshKeys`, `ensureSshKeys`, `sshKeyProvider`. The keys carry no region-slug or
  container label (`tags.HetznerLabels(..., "", "", ...)`) since they are env-scoped, so the single
  shared key is labelled identically regardless of which scope creates it. Per-instance maps
  (`firewalls`, `instanceCounters`) stay per-instance: firewalls are region-scoped (their names embed
  the slug) so they never collide across scopes.
- `internal/registry/registry.go` — `BuildRegistry`'s `sshKeys` parameter and the `NewSSHKeyCache`
  wrapper, threaded into `Compute` → `hetzner.NewCompute`.
- `program/program.go` — the single `registry.NewSSHKeyCache()` passed to every `BuildRegistry`.

When adding any new account-global (region-less, env-scoped) Hetzner resource, give it the same
treatment: a shared cache so it registers once, a fixed (non-region-scoped) name, and a
scope-order-independent owning provider. Do **not** give it a region-scoped name like a regional
resource.

## Why

A single-scope config (one region, or global-only) never exercised the collision — one
`HetznerCompute`, one registration. The first config to provision a server in **both** the global
slice and a region — or in two regions — registered the env-scoped SSH keys twice under one URN and
failed at preview before any resource was touched. This was hit by the first real cross-scope
deployment (`wardnet-infrastructure` PR #31).
