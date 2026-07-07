---
sidebar_position: 7
---

# Runbook: database backups

Self-hosted PostgreSQL databases (ADR-0036) are backed up on their **cluster host** — a self-hosted
cluster is private-only, so backups can only run on the host. Each database gets a systemd timer that
runs `pg_dump -Fc | gzip` and uploads the archive to a dedicated Cloudflare R2 bucket.

- **Object key:** `<env>/<region>/<cluster>/<database>/<timestamp>.dump.gz` (ISO-8601 UTC timestamp;
  `<region>` is the region slug, or `global`). One bucket serves every environment and region, and the
  region segment keeps a regional cluster's per-region backups (and their retention) apart.
- **Data-only:** roles are re-minted by `inforge deploy`, never captured in the dump, so a restore
  needs no role/credential import.
- **RPO = the backup interval.** Point-in-time recovery (WAL archiving) is not yet available.

**Posture:** the infra repo (deploy delivers the credential and installs the timers).

## One-time setup

### 1. Create a dedicated R2 bucket + scoped token

Create an R2 bucket (e.g. `wardnet-backups`) and an **Object Read & Write** API token scoped to that
bucket only. It must be **distinct** from your Pulumi state bucket and your release-artifacts bucket —
`inforge` rejects the config otherwise.

### 2. Point `inforge.yaml` at it

```yaml title="inforge.yaml"
backups:
  bucket: wardnet-backups   # flat bucket name; endpoint derived from CLOUDFLARE_ACCOUNT_ID
```

### 3. Store the backup-scoped credential (reserved secret)

The timer runs on the DB host (not the deploy machine), so it needs its own least-privilege R2
credential, stored as two reserved secrets:

```bash
printf %s "$R2_ACCESS_KEY_ID"     | inforge secret set <env> backups r2_access_key_id --reserved
printf %s "$R2_SECRET_ACCESS_KEY" | inforge secret set <env> backups r2_secret_access_key --reserved
```

Commit the encrypted store and run `inforge deploy <env>`. Deploy installs `awscli` on each cluster
host, writes the credential (`0600`, root), and installs a `wardnet-backup-<cluster>-<database>.timer`
per enabled database.

:::warning Backups are on by default
Every database defaults to `backup.enabled: true`. If a database has backups enabled but no
`backups.bucket` (or credential) is configured, `inforge deploy` **fails** rather than silently leaving
data unprotected, and `inforge validate` **warns** so CI catches it first. Configure the bucket +
credential, or opt the database out with `backup.enabled: false`.
:::

## Per-database policy

Authored on the [database](../resources/database#backups) manifest:

```yaml
backup:
  enabled: true    # false opts a throwaway/derived database out
  interval: 24h    # cadence = RPO
  keep: 7          # newest N archives retained; older pruned each run
```

## Verifying

Trigger a backup now and list the archives with the [`inforge db`](../cli/db) commands (from your
deploy machine — `db backup` SSH-triggers the on-host timer's oneshot, `db list-backups` reads R2 with
your own credentials):

```bash
inforge db backup <env> [<database>]        # run a backup now (all regions, or --region <slug>)
inforge db list-backups <env> [<database>]  # list the R2 archives, grouped by region + database
```

On a cluster host directly:

```bash
systemctl list-timers 'wardnet-backup-*'                          # scheduled backups
sudo systemctl start wardnet-backup-<cluster>-<database>.service  # run one now
journalctl -u wardnet-backup-<cluster>-<database>.service         # inspect a run
```

## Disk-fill early warning

The data volume is the durability-critical resource. The host OTel collector (ADR-0031) already emits
`system.filesystem.utilization` per mount, so no extra code is needed — add a **Grafana Cloud alert**
on `system.filesystem.utilization` for the data-volume mount (`/var/lib/wardnet/db/<cluster>`),
e.g. warn at 80% and page at 90%, so you rebuild/resize the volume before Postgres runs out of space.

## Restore

Restore is driven by [`inforge db restore`](../cli/db#inforge-db-restore). It **SSH-triggers an
on-host `pg_restore`** (the cluster is private-only) and is **data-only**: a single-database dump never
contains roles, so the owner role and per-service login roles are (re)created by `inforge deploy`, not
by the restore. Restore is **destructive** — `pg_restore --clean --if-exists` drops and recreates the
target database's objects — so `inforge db restore` prints the resolved plan and does nothing unless
you pass `--yes`.

### Restore the newest backup

```bash
inforge db restore <env> <database>            # dry run: prints the plan, touches nothing
inforge db restore <env> <database> --yes      # restores the newest R2 backup
```

For a multi-region database, pass `--region <slug>` to pick which region's cluster to restore into
(a restore always targets exactly one host). `--from-key <key>` restores a specific archive (the
region is taken from the key, so `--region` is not needed).

`--from-key`/newest restore downloads the archive **on the host** using the backup-scoped R2 credential
already delivered there — so it only works on a host that has backups provisioned. Restore into a
**quiescent** database (no application connections), then run `inforge deploy <env>` to re-mint roles
and re-apply grants.

### Restore (or seed) from a local dump — `--from-dump`

`--from-dump` uploads a local `pg_dump -Fc` file to the host over SSH (no R2, so it works even on a
fresh cluster with no backups configured) and restores it. This is the manual-migration / seed path:

```bash
pg_dump -Fc "$SOURCE_DSN" -f app.dump           # custom-format dump from the source
inforge db restore <env> <database> --from-dump app.dump --yes
inforge deploy <env>                             # re-mint roles + re-apply grants
```

The dump must be a **custom-format** (`-Fc`) file — `inforge` gzips it before streaming, matching the
backup format. The uploaded file is removed from the host after the restore.

### Proof the backups are usable

The dump→restore round-trip is covered by an automated test that stands up a real throwaway Postgres,
runs the exact `pg_dump -Fc | gzip` and `gunzip | pg_restore --clean --if-exists` pipelines the tooling
emits, and asserts the restored rows **and object ownership** match — and that a re-restore is
idempotent (`internal/dbbackup/restore_roundtrip_test.go`, `go test ./internal/dbbackup/`). It runs
whenever the Postgres client binaries are on `PATH` and skips cleanly otherwise.
