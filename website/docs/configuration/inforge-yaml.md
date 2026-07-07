---
sidebar_position: 1
---

# inforge.yaml

The `inforge.yaml` file at the repo root is the project-level configuration. Every
`inforge` command reads it (unless `--config` overrides the path).

## Full schema

```yaml
name: my-infra            # required — Pulumi project name (alphanumeric + hyphens)

backend:
  type: file              # required — one of: file, git-branch, s3, r2
  url: file://.pulumi     # used for "file" and "s3" types
  branch: pulumi-state    # used for "git-branch" type

providers:                # optional — project-level provider defaults
  compute: hetzner        # default provider for all Compute resources
  database:
    postgresql: self-hosted   # default provider for postgresql database-clusters

backups:                  # optional — self-hosted Postgres backup destination
  bucket: wardnet-backups # a dedicated R2 bucket (distinct from state + artifacts)
```

## Fields

### `name`

The Pulumi project name. Must be unique per backend. Alphanumeric characters and hyphens only.

### `backend`

Controls where Pulumi state is stored.

#### `type: file`

State stored in a local directory. Recommended for getting started.

```yaml
backend:
  type: file
  url: file://.pulumi    # relative path from repo root
```

#### `type: git-branch`

State stored on a dedicated git branch. inforge fetches the branch before apply
and commits+pushes after a successful apply. Keeps state history out of the main branch.

```yaml
backend:
  type: git-branch
  branch: pulumi-state   # remote branch name (created if it doesn't exist)
```

#### `type: s3`

State stored in an S3-compatible bucket. Uses standard AWS credentials or
environment variables.

```yaml
backend:
  type: s3
  url: s3://my-bucket/inforge-state
```

Requires `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and `AWS_REGION` env vars,
or an IAM role.

#### `type: r2`

State stored in Cloudflare R2 (S3-compatible). inforge translates `r2://` to the
correct S3-compatible endpoint automatically.

```yaml
backend:
  type: r2
  url: r2://my-r2-bucket
```

Requires `CLOUDFLARE_ACCOUNT_ID` environment variable. R2 credentials are provided via
`AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (R2 API token).

### `providers`

An optional project-level block that sets default provider names for each resource class. Resources
that omit their `provider:` field inherit from here; an explicit `provider:` on a resource always
takes precedence.

```yaml
providers:
  compute: hetzner
  database:
    postgresql: self-hosted
```

- **`compute`** — default provider name for all Compute resources. Network resources inherit this
  same default (there is no separate `network` key).
- **`database.<engine>`** — default provider for [database-clusters](../resources/database-cluster) of
  that engine. When unset it defaults to **`self-hosted`** — inforge installs and runs the engine on a
  compute host you provision (ADR-0036).

When `providers:` is omitted every Compute and Network resource must declare `provider:`
explicitly. Secrets need no provider configuration at all: inforge resolves a service's
`vault:`/`ref:`/grant values at deploy time and age-encrypts them directly to the target host's own
SSH key, delivering them over the same SSH connection inforge already uses to provision the host.

### `backups`

The destination for self-hosted PostgreSQL backups (ADR-0036). Optional — only cluster hosts running
a database with backups enabled read it.

```yaml
backups:
  bucket: wardnet-backups   # a dedicated Cloudflare R2 bucket name
```

- **`bucket`** — the R2 bucket name per-database `pg_dump` archives are uploaded to. It is authored
  **flat** (a bare bucket name, not an `r2://` URL or a path — inforge rejects a value containing `:` or
  `/`); the R2 endpoint is derived from `CLOUDFLARE_ACCOUNT_ID`, exactly like the state/artifacts stores.
  Objects are keyed `<env>/<region>/<cluster>/<database>/<timestamp>.dump.gz`, so **one bucket serves
  every environment and region** (the region segment keeps a regional cluster's per-region backups
  apart).

The backups bucket must be **distinct from both the state bucket and the artifacts bucket** — inforge
rejects the config otherwise. Backups hold sensitive full data with their own retention lifecycle, so
they never colocate with Pulumi state or release tarballs.

Backups need a **backup-scoped R2 credential** delivered to each cluster host — the two reserved
secrets `backups/r2_access_key_id` and `backups/r2_secret_access_key` (see
[`inforge secret --reserved`](../cli/secret#reserved-secrets---reserved) and
[Database — Backups](../resources/database#backups)). A `backups.bucket` set with no credential fails
the deploy; a database with backups enabled and **no** `backups.bucket` configured also fails (set the
bucket, or opt the database out with `backup.enabled: false`).

## Minimal example

```yaml
name: my-infra
backend:
  type: file
  url: file://.pulumi
```
