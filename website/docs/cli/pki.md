---
sidebar_position: 7
---

# inforge pki

Manage an environment's **git-committed encrypted PKI store** — the certificate hierarchies that
secure the service mesh (and a daemon's trust tree). The store lives at `resources/<env>/pki.enc.yaml`,
the structural twin of the [secret store](/cli/secret): certificates are committed as **plaintext PEM**
(they are public), private keys as **age ciphertext**. See
[ADR-0024](https://github.com/wardnet/inforge/blob/main/docs/adr/0024-inforge-pki-service-mesh-and-daemon.md).

:::info Two key custodians
The store records two age recipients. The **offline root recipient** owns cold two-tier root keys —
its identity is kept offline by an operator and **never reaches CI**, so CI literally cannot sign with
a root. The **CI recipient** (the same one the secret store uses, `INFORGE_SECRETS_KEY`) owns
intermediate and root-only issuer keys, which deploy and `inforge pki renew` use.
:::

## Subcommands

| Command | Purpose |
|---------|---------|
| `inforge pki init <env>` | Create the env's store. Reuses the secret store's CI recipient; generates the offline root recipient and prints its identity **once**. |
| `inforge pki add <env> <name> --topology two-tier\|root-only` | Generate a PKI root and record it. A two-tier root is **cold** (encrypted to the offline recipient); a root-only root is delivered to an online issuer (encrypted to CI). |
| `inforge pki intermediate <env> <name> <scope>` | **Offline, operator-run.** Mint a per-scope intermediate CA for a two-tier PKI, signed by its cold root. Needs the offline root identity in `INFORGE_PKI_ROOT_KEY`. |
| `inforge pki rotate <env> <name> --leaf\|--intermediate <scope>\|--root` | Rotate a tier. `--leaf` documents leaf renewal; `--intermediate` re-mints one scope's intermediate from the cold root (**offline**); `--root` runs a dual-root overlap (`--finalize` ends it). |
| `inforge pki recover-intermediate <env> <name> <scope>` | **Offline.** Compromise recovery for one intermediate: fresh-key re-mint + forced, immediate host re-projection. |
| `inforge pki renew <env>` | Mint fresh mesh **leaf** certificates for every service and write them to the secrets provider. Decoupled from `inforge deploy`. |
| `inforge pki ls <env>` | List the PKIs in the store with their topology and the tiers present. |

:::tip Operator runbooks
Step-by-step procedures for adding a region and for rotating or recovering each tier live in the
[PKI runbooks](/runbooks/pki).
:::

## Topologies

- **`two-tier`** — the service **mesh**: a cold (offline) root plus one intermediate per active scope
  (the `global` scope, and each region in `regions.yaml`). inforge mints short-TTL leaves from the
  scope's intermediate at renew time. A service joins a mesh via its [`pki:` field](/resources/service).
- **`root-only`** — a single root with no intermediate, delivered to a designated online issuer (e.g. a
  daemon). inforge does not mint leaves for it.

## Scopes and the regional boundary

A two-tier mesh keys its intermediates by **scope**: the literal `global`, or an abstract region name
(e.g. `us-east-1`). A service's leaf is minted from **its** scope's intermediate — a global service
from `global`, a regional service from each region it deploys to. Each service is also delivered the
**trust bundle** of the scopes it may talk to (a regional service trusts `{its region, global}`; a
global service trusts `{all regions, global}`), which enforces the mesh's regional boundary:
intra-region ✅, region→global ✅, global→global ✅, **cross-region ❌, global→region ❌**.

## Minting intermediates (offline)

Intermediates are signed by the **cold root**, so this step runs **offline** with the root identity
that `inforge pki init` printed once:

```bash
export INFORGE_PKI_ROOT_KEY="AGE-SECRET-KEY-…"   # the offline root identity, kept offline
inforge pki intermediate prd wardnet-mesh global
inforge pki intermediate prd wardnet-mesh us-east-1
```

Commit the resulting `pki.enc.yaml`. `inforge validate` fails if a service's `pki:` references a mesh
with no intermediate for one of the service's scopes.

## Renewing leaves

```bash
export INFORGE_SECRETS_KEY="AGE-SECRET-KEY-…"   # the CI master identity
inforge pki renew prd
```

`inforge pki renew` mints a fresh leaf for every mesh service (one per scope) and writes the material
to the secrets provider:

- **Per mesh host**, every co-located service's leaf + key and the host's trust bundle, under
  `/<hostKey>/` in the scope's shared **mesh workspace** — this is the copy the per-host **mesh proxy**
  serves east-west traffic with (the proxy, not the service, holds mesh cert material).
- **Per `mtls_files: true` service** (the raw-mTLS-plane exception, e.g. a node↔node forward listener),
  additionally the service's own copy under `/<service>/mtls`.

Leaves are valid for **90 days** and carry a SPIFFE identity
(`spiffe://<base-domain>/<env>/<scope>/<service>`) so peers can authorize on the encoded scope.

:::info Renew is not a deploy — schedule it
`inforge pki renew` **only** writes cert material to the provider; it never runs the infra program and
never connects to a host, so it is safe to run while your working tree has un-shipped infra changes.
Every run re-mints a fresh 90-day leaf, so the effective rotation interval is how often you run it —
**schedule it on a cron** (e.g. weekly, well inside the 90-day window) so leaves never approach expiry.
The workspaces must already exist — run `inforge deploy` for the environment first; renew adopts them,
never creates them.
:::

## How a renewed leaf reaches a running host

Renewal is pull-based: each consumer has a daily systemd timer that re-fetches its material from the
provider and applies it **only if it changed**.

- **The mesh proxy** (every host running ≥1 `pki:` service) has a `wardnet-mesh-renew.timer` running
  `inforge-agent mesh-project`: it fetches the host's own `/<hostKey>/` path (with a per-host identity
  that can read only that path) and atomically projects every co-located service's leaf + the trust
  bundle into the proxy's tmpfs, then reloads the mesh nginx — no downtime, and the services themselves
  are untouched (they hold no cert material). The same projection runs at proxy start, so a **reboot
  re-seeds real material** before nginx accepts traffic.
- **An `mtls_files: true` service** additionally has a per-service `wardnet-<svc>-renew.timer` running
  `inforge-agent project`, which re-projects `/<svc>/mtls` into the service's tmpfs and then
  **reloads** the unit when it declares a [`reload:`](/resources/service) command, else **restarts** it.

So renewal is hands-off: `inforge pki renew` writes the provider, and each host converges on its own
within the timer interval (well inside the 90-day TTL), with no CLI→host connection required. Leaf
private keys live only in tmpfs (RAM) for the life of a boot — never written to persistent disk.

### The deploy baseline

Right after `pulumi up`, `inforge deploy` (and `inforge ephemeral up`) runs a **mesh baseline** step:
it mints the environment's mesh material with the same core as `inforge pki renew`, then SSHes each
mesh host and starts its `wardnet-mesh-renew` oneshot — so the freshly (re)configured proxies leave
their self-signed placeholder certificates immediately instead of waiting for the daily timer. The
trigger pushes a *signal*, never cert material. It needs the deploy SSH key (`--ssh-key` or
`INFORGE_DEPLOY_KEY`); if a trigger fails, the material is already in the provider and the host
converges on its own timer.

### The release-time mint (`mtls_files` services only)

An `mtls_files: true` service only starts running on its **first `inforge releases deploy`**. Because
its boot path projects whatever the provider holds, that first start would crash-loop until the daily
renew timer first fired. To close the gap, `inforge releases deploy` mints the released service's leaf
**before** it restarts the unit. Every other service — mesh member or not — skips this step: a plain
mesh member's leaf is delivered to the mesh proxy by deploy/renew, independent of releases.

## Rotation and recovery

Each tier rotates on its own cadence and with its own custody:

- **Leaves** are short-TTL — re-minting (`inforge pki renew`) *is* rotation; expiry is revocation.
  See [rotate a leaf](/runbooks/pki-rotate-leaf).
- **Intermediates** rotate from the cold root (offline). A planned roll
  (`inforge pki rotate <env> <name> --intermediate <scope>`) is invisible to other regions thanks to
  the regional boundary; a suspected key leak uses `inforge pki recover-intermediate` for an
  immediate, no-overlap replacement. See [rotate an intermediate](/runbooks/pki-rotate-intermediate)
  and [recover a compromised intermediate](/runbooks/pki-recover-intermediate).
- **The root** rotates with a **dual-root overlap** (`inforge pki rotate <env> <name> --root`, then
  `--root --finalize`): both roots verify during the window while root-anchoring consumers (e.g. the
  daemon fleet, cross-repo) come to trust the new root. Mesh services are undisturbed — re-signing
  preserves intermediate keys. See [rotate the root](/runbooks/pki-rotate-root).

## The store file

```yaml title="resources/prd/pki.enc.yaml"
# managed by `inforge pki` — do not edit by hand
rootRecipient: age1…        # offline operator key — cold two-tier root keys encrypt to it; CI never holds it
recipient: age1…            # the CI recipient (== the secret store's) — intermediate / root-only keys encrypt to it
pkis:
  wardnet-mesh:
    topology: two-tier
    root:
      cert: |               # plaintext PEM (public)
        -----BEGIN CERTIFICATE-----
      key: |                # age ciphertext → rootRecipient (cold)
        -----BEGIN AGE ENCRYPTED FILE-----
    intermediates:
      global:
        cert: |             # plaintext PEM
        key: |              # age ciphertext → recipient (CI)
      us-east-1:
        cert: |
        key: |
    # During a `--root` overlap only, the store also carries the retired tier:
    # previousRoots:                 # old root cert+key, dropped on --finalize
    #   - cert: |
    #     key: |
    # previousIntermediates:         # old intermediate certs (per scope), public
    #   global:
    #     - |
```

Because every certificate is committed in the clear, `inforge validate` checks a service's `pki:`
references against the store **credential-free** — no decryption keys needed, the same model as
`vault:` against the secret store.
