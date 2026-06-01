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

escrow:
  url: https://inforge-escrow-worker.workers.dev
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

### `escrow.url`

The URL of the inforge escrow service. Defaults to the centrally-operated inforge escrow.

```yaml
escrow:
  url: https://inforge-escrow-worker.workers.dev
```

This value is also set per-stack in `inforge.<env>.yaml` as `inforge:escrow_url` so it
is passed into the Pulumi program at deploy time.

## Minimal example

```yaml
name: my-infra
backend:
  type: file
  url: file://.pulumi
escrow:
  url: https://inforge-escrow-worker.workers.dev
```
