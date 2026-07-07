---
sidebar_position: 10
---

# inforge db

Operate on the [self-hosted PostgreSQL](../resources/database) backups (ADR-0036): trigger a backup,
list the archives, and restore a database. A self-hosted cluster is **private-only**, so backups and
restores run **on the cluster host** — the `db` commands are a **signal push over SSH** (they trigger
on-host work, never ship database material over the wire) and never run Pulumi. See the
[database backups runbook](../runbooks/database-backups).

Targets are resolved from the `dbDeployDescriptor` stack output, so `inforge deploy` must have run for
the environment first. Backups are keyed `<env>/<region>/<cluster>/<database>/<timestamp>.dump.gz`.

## Subcommands

| Command | Purpose |
|---------|---------|
| `backup` | Trigger an on-host backup now for one or all databases |
| `list-backups` | List the R2 archives for one or all databases |
| `restore` | Restore a database from an R2 backup or a local dump (data-only) |

## `inforge db backup`

```bash
inforge db backup <env> [<database>] [--region <slug>] [--ssh-key <path>]
```

SSH-triggers the on-host backup oneshot (`pg_dump -Fc | gzip` → R2) for `<database>`, or every
backup-enabled database when omitted. It **fans out across all regions** by default; `--region`
narrows to one. A database with `backup.enabled: false` has no backup unit and is skipped (naming one
explicitly is an error). The oneshot runs synchronously, so a failed dump fails the command.

## `inforge db list-backups`

```bash
inforge db list-backups <env> [<database>] [--region <slug>]
```

Lists the R2 archives for `<database>` (or every database when omitted), grouped by region and
database, newest last. It reads R2 with **your own** `AWS_*` credentials (the deploy machine has full
R2 access; the host holds only the backup-scoped write credential).

## `inforge db restore`

```bash
inforge db restore <env> <database> [--from-key <key> | --from-dump <file>] [--region <slug>] [--yes] [--ssh-key <path>]
```

SSH-triggers an on-host `pg_restore`. **Data-only** — roles are re-minted by `inforge deploy`, never
captured in a single-database dump — so run `inforge deploy <env>` afterwards to re-apply grants.
**Destructive** (`pg_restore --clean --if-exists` drops and recreates objects), so without `--yes` it
prints the resolved plan and exits without touching the database.

- **Default** — restores the **newest** R2 backup.
- **`--from-key <key>`** — restores a specific archive; the region is taken from the key.
- **`--from-dump <file>`** — uploads a local `pg_dump -Fc` file to the host over SSH (no R2, so it
  works on a fresh cluster with no backups) and restores it. The file is removed from the host after.
- **`--region <slug>` / `--cluster <name>`** — a restore always targets exactly one host, so when a
  database name resolves to more than one target (multiple regions, or the same database name in two
  clusters of one scope) pass whichever discriminators are needed to pick one. Not needed with
  `--from-key`, which names both the region and the cluster.

`--from-key`/newest restore downloads the archive on the host using the backup-scoped R2 credential,
so it requires backups to be provisioned there; `--from-dump` does not.

```bash
inforge db restore prd app                    # dry run: prints the plan
inforge db restore prd app --yes              # restore the newest backup
inforge db restore prd app --from-dump app.dump --yes   # seed from a local dump
```

## Common flags

| Flag | Meaning |
|------|---------|
| `--region <slug>` | Restrict `backup`/`list-backups` to one region; pick the region for `restore`. |
| `--cluster <name>` | Restrict `backup`/`list-backups` to one database-cluster; disambiguate a repeated database name for `restore`. |
| `--ssh-key <path>` | SSH deploy key (overrides `INFORGE_DEPLOY_KEY`). |
| `--stack-config <path>` | Infra stack config file (default `inforge.<env>.yaml`). |
| `--yes` | (`restore` only) execute — without it, only the plan is printed. |
