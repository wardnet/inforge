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
    postgresql: neon      # default provider for postgresql Database resources
  secretsStore: infisical # authoritative secrets provider for this project
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
    postgresql: neon
  secretsStore: infisical
```

- **`compute`** — default provider name for all Compute resources.
- **`database.<engine>`** — default provider for databases of that engine (e.g. `postgresql: neon`).
- **`secretsStore`** — the project's authoritative secrets provider. There is no per-resource
  override; this is a project-wide selection. `inforge validate` checks that the named provider's
  credentials appear in every region that has secret-bearing services.

When `providers:` is omitted every resource must declare `provider:` explicitly.

## Minimal example

```yaml
name: my-infra
backend:
  type: file
  url: file://.pulumi
```
