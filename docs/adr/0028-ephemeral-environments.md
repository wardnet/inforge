# Ephemeral (preview) environments: identity decoupled from config source

Wardnet deploys are infrequent, so a permanently-running test environment is not justified — yet there
is a real need to spin up a faithful, isolated copy of an existing environment on demand, run actual
end-to-end tests (typically validating an infra-definition change in a PR), then tear it down. The
provider constraint shapes the grain: **Hetzner bills a server while the server object exists, even
powered off** — billing stops only on *delete*. So "off" must mean *destroyed*; create-and-destroy is
the natural cycle, and auto-teardown is the central cost control.

This ADR adds an `inforge ephemeral up | down | reap` command group (alias `eph`) that clones a source
environment's **definition** into a fresh, network-segregated environment under a generated identity,
deploys the **exact versions currently live in the source**, and guarantees teardown via a TTL reaped
out-of-band.

## Identity is decoupled from the config source

Before this change, an env's name was simultaneously its identity (the naming segment, FQDNs, labels,
trust scope) **and** its config source (the `resources/<env>/` tree it reads). An ephemeral env must
clone a source's *definition* while keeping its *own* identity, so the two are split:

- **`environment`** (= the stack name = the slug) stays the **identity**: every cloud name
  (`wardnet-<slug>-…`), FQDN, Neon/Infisical/Hetzner resource, tag, and SPIFFE scope uses it.
- **`source_environment`** (new stack config, defaulting to `environment`) is the **config source**:
  only the loaders (`LoadVariables`, `LoadRegionTable`, `LoadResources`, `LoadGlobalResources`), the
  encrypted-secret decrypt, and the PKI store path read it.

Because `source_environment` defaults to `environment`, every existing static deploy is byte-for-byte
unchanged. An ephemeral env sets `source_environment = <src>` and `environment = <slug>`, so it reads
the source's definition but realizes it under its own isolated identity — isolation is automatic, by
construction, everywhere downstream.

The split lives in `program.Run`. Everything else (naming, FQDNs, Neon, Infisical, Hetzner, tags, mesh
SPIFFE IDs) keeps using `environment`. The mesh-cert path is split the same way
(`renewMeshCertsAs(configEnv, identityEnv)`): the `pki.enc.yaml` store and the intermediate come from
`source_environment`, while the minted leaf's SPIFFE ID and its Infisical workspace use the slug — so an
ephemeral service gets its own trust scope (`spiffe://…/<slug>/…`) signed by the source's intermediate.

## The one real gap: `AppFQDN`

`naming.AppFQDN` deliberately omits the env segment (ADR-0026: each env carries its own `base_domain`,
so an app is `<subdomain>.<base>` / `<subdomain>.<region>.<base>`). A clone *inherits the source's
base_domain*, so it would collide with the source on the app hostname. Fix: **for ephemeral envs only**,
`AppFQDN` inserts the slug segment right after the subdomain (`<subdomain>.<slug>.<base>` global /
`<subdomain>.<slug>.<region>.<base>` regional), keyed on the same ephemeral flag threaded for
TTL/labels. The flag+slug are threaded to `resolveIngressApps`/DNS/nginx/cert derivations (they share one
resolver path per ADR-0026) so the A-record, the cert SNI, and the nginx server block all agree. Static
envs are untouched; there is no "prod" concept.

## `up` = provision + replicate-deploy, in one command

`up` takes no service/SHA arguments — it always reproduces the source's current versions:

1. **Provision**: a Pulumi up of the source config tree under the slug identity.
2. **Replicate**: for every service and app *defined in the source config*, read the source's per-host
   manifest (`release.Store.LoadManifest(name, source_environment)`), resolve each ephemeral host's
   source counterpart (env-label swap on the host DNS), and deliver that host's SHA via the existing
   `deliverRelease` transport — writing the ephemeral env's own `manifest.<slug>.yaml`.

It is **per-host faithful** (each ephemeral host gets the SHA its source counterpart runs) and
**skip-and-reports** a workload not yet deployed in the source (no manifest entry) rather than failing
the `up`: an undeployed app keeps its placeholder seed. No rebuild — artifacts are env-agnostic, keyed
by SHA; the ephemeral manifest pins the SHAs so pruning won't GC them while the env is live.

## TTL and the three-signal reaper

`up` writes `expires_at = now + ttl` (epoch seconds — Hetzner label values forbid the `:` of RFC3339)
to stack config **before** the Pulumi run, so a crashed `up` still leaves a reap-able partial stack.
`--ttl` defaults to 2h, hard-capped at 24h (overridable via `ephemeral.maxTtl` in `inforge.yaml`).

`reap` enumerates candidates with `ListStacks` against the bucket backend and classifies each purely
from its persisted stack config — **no program run**. A stack is reaped iff
`ephemeral == "true"` **AND** `expires_at` is in the past. Both are written only by `up`, so **no
permanent stack can ever match** — the three-signal guarantee. `reap` destroys by default (cron-friendly,
no confirmation); `--dry-run` only lists. The Hetzner `ephemeral`/`expires_at` labels mirror stack config
purely for orphan auditing — they are never the classification source.

## State backend is a hard requirement

The ephemeral commands require an `s3`/`r2` Pulumi backend and **hard-fail** on `git-branch`/`file`:
per-stack object keying gives concurrent `up`s isolation, and `ListStacks` enumeration is what `reap`
walks. The git-branch backend serialises all state into one branch tree (no per-stack keying); the file
backend is single-host. Neither supports the enumerate-and-reap model.

## Network segregation is a structural invariant

Each env provisions its **own Hetzner Network** (`naming.Resource(env, slug, "net", …)`), and there is
**no peering anywhere**. So an ephemeral host cannot route to a real env's private IPs even with an
identical inherited CIDR — unpeered Networks don't talk. This is codified, not coded: see
`.agents/rules/ephemeral-network-segregation.md`. Shared accounts (one Hetzner project / Neon / Cloudflare
zone / Infisical org) are isolated by the slug in every name plus the `ephemeral=true` label.

## Risks / watch-items

- **Cold destroy needs the source config present.** `down`/`reap` re-run the inline program to resolve
  the graph; if `resources/<src>/` was deleted from the checkout, destroy can't resolve. Fallback for
  true orphans: a tag-based Hetzner sweep using the `ephemeral`+`expires_at` labels (manual).
- **Neon/Infisical carry no tags.** Reaping leaked managed resources after state loss is name-prefix-only;
  the Pulumi-state teardown path is primary, so this is a fallback only.
- **Effective lifetime = `ttl + reap interval`.** Consumers should cron `reap` every ~15–30 min so an
  env does not outlive its TTL by much.
