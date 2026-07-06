# 36. Self-hosted Postgres, and splitting `database` into cluster + logical database

## Context

Neon's free tier is a hard, recurring constraint. Our database usage is low (far from
10 GB). A ~$1/mo 10 GB Hetzner volume on a small host runs it comfortably, and self-hosting
Postgres is boring, proven, and cheap. We replace Neon as the Postgres implementation while
**keeping** the `DatabaseProvider` / `DBRoleProvisioner` seam so a managed provider can be
re-added later — this is a replacement of the *implementation in use*, not a removal of the
abstraction (the cheap-exit guarantee).

Doing this surfaced two things the current model got wrong or lacked, which this ADR fixes
alongside the provider swap:

1. **`database` conflates two concepts.** Today one `database` resource carries both `engine`
   (the running Postgres *instance*) and `database`/`owner` (a *logical* database inside it).
   That made it impossible to put two logical databases (e.g. `ddns` and `tunneller`) on one
   Postgres instance without standing up two instances.
2. **No persistent-volume primitive exists.** Compute provisions a server + boot disk only —
   there is no block storage that survives a VM rebuild, which self-hosting a stateful database
   requires.

## Decisions

### Split `database` into `database-cluster` + `database`

- **`database-cluster`** (new resource type, `…/database-cluster/<name>/`) is the single-instance
  engine: `engine`, a same-scope compute `host:` FK, `provider`, and engine `version`. Its data lives
  on a single persistent volume whose size is **derived** from its databases (not authored on the
  cluster). It is explicitly **single-instance** — no HA/replication — despite the
  word "cluster" (PostgreSQL's own term for one `initdb` instance managing several databases).
- **`database`** (re-modelled) is a *logical* database inside a cluster: a `cluster:` FK
  (same scope), `database` name, `owner`, an optional per-database `size`, and an optional
  per-database `backup` policy. It drops `engine`/`provider`/`branch` (those move to the cluster).
- Co-location works both ways: many logical databases in one cluster (shared volume), and many
  clusters on one host (each its own volume, derived TCP port, and systemd unit). Host↔cluster is
  **not** forced 1:1.

### Provider discrimination and the host FK

- Reuse the existing `provider:` discriminator (data-driven `ProviderDefaults.Database[engine]` +
  the registry switch) — add the value **`self-hosted`** and flip the default to
  `postgresql: self-hosted`. No new `type:` field: a second discriminator could contradict
  `provider:` and duplicates what the provider string already encodes.
- The `host:` FK (on the **cluster**) is **required** when the resolved provider is self-hosted,
  **forbidden** when managed. It is resolved through the shared `resolveComputeHost` helper (rule
  `use-resolve-compute-host-for-host-fk`).

### Storage is a provider-neutral primitive; the volume is per-cluster

- Storage is modelled provider-agnostically (Hetzner Volume today, AWS EBS/etc. later), like the
  `DatabaseProvider` seam. `size` is authored on the **logical database** as *declared intent*; a
  provider realizes it as its engine allows. **Postgres has no native per-database quota**, so
  inforge sizes the cluster's **single volume** as `max(Σ database sizes, providerMinimum)`
  (Hetzner volumes start at 10 GB) and per-database size is a documented no-op on Postgres. Engines
  that support it (a future SQL Server provider) would enforce per-database.
- The **entire** cluster PGDATA lives on that one volume, mounted at an inforge-controlled path
  keyed by the cluster name. This makes durability literal: reattach the volume to a rebuilt VM →
  full recovery of every database in the cluster. The volume is a separate `hcloud.Volume`
  (not the boot disk) and is **attached after the cloud-init readiness gate** — a separate
  attachment resource `DependsOn` the gate — exactly mirroring the private-network two-step
  (rule `attach-private-network-after-cloud-init-gate`); an on-host idempotent mkfs/mount/fstab
  step follows the attach.

### Role minting runs on the host, not from the deploy machine

This is the pivotal constraint. Postgres listens on the host's **private** IP only (5432 opened
to the private CIDR only — the private-only firewall, rule `exposed-ports-are-private-only`), so it
is **unreachable from the deploy machine** (GitHub Actions / an operator laptop are not on the
Hetzner private network). Neon's `RoleProvisioner` ran pgx *from the deploy machine* against Neon's
*public* endpoint — that model cannot work self-hosted. Therefore:

- Per-service role minting (`CREATE ROLE` + the `ro`/`rw` `GRANT`s) runs **on the cluster host**
  via `remote.NewCommand` → `sudo -u postgres psql` (local **peer** auth over the unix socket).
- We reuse Neon's ro/rw **GRANT SQL text** (extracted to a shared `internal/pgrole` package) but
  **drop the pgx transport**. pgx is no longer needed for self-hosted.
- The owner/admin-DSN-off-host problem **dissolves**: local peer auth needs no owner password
  leaving the host.
- Per-service passwords are generated with `random.RandomPassword` (stable across deploys,
  encrypted in state) and returned as `DBRoleFields{User, Password, Host=<cluster host private IP>,
  Port=<cluster port>, DBName=<logical db>, URL}`.

### Grants are unchanged — the cheap-exit guarantee (de-risking requirement #1)

A grant still targets `database/<name>` (the *logical* database) and still resolves through a
provider-neutral `DBRoleProvisioner`; the logical database resolves through its cluster for the
actual role. **Consumer service manifests do not change** — `grants:` entries, `{URL}`/`{USER}`…
fields, and the agent's secret-fetch path are all untouched. The only place the seam evolves is the
provider realization (now cluster-granular), which still yields one `DBRoleProvisioner` per logical
database. No self-hosted-specific coupling leaks into consumers.

### Backups (on-host) and restore tooling

- Backups **must** run on the host (private-only). A per-database `pg_dump -Fc | gzip` → R2 driven by
  a per-database systemd timer. Roles are re-minted by deploy, not captured in the dump, so a restore
  is data-only.
- **Destination** is a **dedicated** R2 bucket authored flat as `backups.bucket` in `inforge.yaml`
  (endpoint via `CLOUDFLARE_ACCOUNT_ID`, like artifacts), validated **distinct from both the state and
  the artifacts buckets** — extending ADR-0016's bucket-separation to a third store, since backups are
  sensitive full data with their own lifecycle/retention. Object key:
  `<env>/<cluster>/<database>/<timestamp>.dump.gz` (env in the key → one bucket serves all envs).
- **Host credential.** Because the timer runs on the cluster host (not the deploy machine, which holds
  the `AWS_*` R2 creds), a **backup-scoped** R2 token is authored under the reserved inforge-internal
  secret namespace (#169, like `observability/otlp_auth`) via `inforge secret set <env> backups <KEY>
  --reserved`, decrypted once at deploy and delivered `0600` to each cluster host. Least-privilege: the
  DB host can write backups and nothing else. The credential is stored as **two keys** —
  `backups/r2_access_key_id` and `backups/r2_secret_access_key` — mirroring the `AWS_*` chain rather
  than one colon-joined value, so the on-host script needs no delimiter parsing and a secret containing
  a `:` is never mis-split. Both are registered in `cmd/inforge`'s `knownReservedSecrets` in the backup
  slice.
- Backup policy is authored **per database** (`backup: {enabled, interval, keep}`; default enabled,
  24 h, keep 7) with explicit **opt-out** (`enabled: false`) for throwaway/derived databases.
  RPO = the backup interval; WAL/PITR is deferred.
- `inforge db backup | list-backups | restore` are first-class commands; restore SSH-triggers an
  on-host `pg_restore` from an R2 backup key (the signal-push pattern of `meshBaseline`).
  `restore --from-dump <file>` uploads a local dump then restores — the path the Neon→self-hosted
  migration uses.

### Disk-fill early warning (de-risking requirement #2)

The OTel collector's `filesystem` scraper (ADR-0031) already emits `system.filesystem.utilization`
per mount, so the volume mount is covered **with no code** — the ADR documents a concrete Grafana
Cloud alert rule on `system.filesystem.utilization` for the data-volume mount as the operational
deliverable.

### Postgres version pin

The cluster's `version` field pins the engine major; install is from the PGDG apt repo, version-
pinned and idempotent, mirroring `internal/otelcol`'s version-pin + upgrade posture. Default is a
conservative recent major, operator-overridable.

### Neon retirement

Extract the reusable ro/rw GRANT SQL to `internal/pgrole` first, then **delete `providers/neon`**
(the adapter and the `pulumi-resource-neon` plugin, whose `NeonProject`/`NeonDatabase`/`NeonRole`
don't fit the cluster/database model anyway), its goreleaser + `plugins.go` entries, the Neon docs,
and the `neon-role-diff` rule. The **seam** (`DatabaseProvider`/`DBRoleProvisioner`) stays, so a
future managed provider re-adds cleanly; git history preserves Neon for reference.

## Consequences

- **New resource type** (`database-cluster`) and a re-modelled `database`; new schemas, loader
  folders, validation, docs (`website/docs/resources/database.md` rewrite + a new
  `database-cluster.md`), and split test fixtures.
- **New persistent-volume primitive** on compute/hetzner (`hcloud.Volume` + attachment, on-host
  mkfs/mount) — the first block storage inforge manages.
- **New dependency:** `pulumi-random` (a standard Pulumi plugin) for stable per-service passwords.
- **Descriptor / `inforge-agent` untouched.** Role minting is deploy-time on-host (Pulumi
  `remote.NewCommand`), and consumers fetch per-service creds via the unchanged grant→Infisical→
  agent path. The cluster host runs Postgres + backup timers as systemd units written at deploy; it
  runs no inforge-agent for Postgres.
- **The cutover is a dump/restore**, executed by the operator against their deployment repo using
  the shipped tooling: `pg_dump` from Neon → `inforge db restore --from-dump` → re-point specs to
  the new cluster/host → `inforge deploy` (roles re-mint per service, consumers unchanged) → verify
  → remove Neon config (`providers.neon.apiKey`/`NEON_API_KEY`). A **tested restore runbook**
  (de-risking requirement #3 — proven, not assumed) is a first-class deliverable and doubly
  load-bearing because the cutover itself is a restore.

### Accepted trade-offs

- **No HA/failover** — single-instance Postgres; if the host dies the DB is down until restored.
  Acceptable at this scale.
- **RPO = backup interval** unless WAL archiving is added later.
- inforge inherits basic Postgres ops (rare major upgrades; OS patching via unattended-upgrades;
  autovacuum handles bloat at low write volume).
- **Durability, not availability, is the real risk:** a host rebuild must not lose data → the
  persistent volume + backups are load-bearing.
- A cluster host serving services on other hosts is a shared dependency — the same SPOF shape Neon
  already was.
