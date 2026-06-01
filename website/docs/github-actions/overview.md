---
sidebar_position: 1
---

# GitHub Actions Overview

inforge ships a set of reusable GitHub Actions workflows and a composite install action.
Consumer repos call these with a single `uses:` line — no inforge-specific tooling needs to
be installed manually.

## Install action

```yaml
- uses: wardnet/inforge/.github/actions/install@v1
```

Downloads the latest `inforge` binary and runs `inforge plugins install`. Accepts a
`version` input to pin to a specific release.

```yaml
- uses: wardnet/inforge/.github/actions/install@v1
  with:
    version: v1.2.0   # pin to a specific release
```

## Reusable workflows

| Workflow | Trigger | Description |
|----------|---------|-------------|
| `validate.yml` | `workflow_call` | Validate changed environments |
| `preview.yml` | `workflow_call` | Preview infrastructure changes |
| `deploy.yml` | `workflow_call` | Deploy infrastructure changes |
| `reconcile.yml` | `workflow_call` + schedule | Detect and fix drift |
| `deploy-raw.yml` | `workflow_call` | Push service code to a provisioned VM |

## Typical consumer setup

In your infrastructure repo, create three workflow files:

```yaml title=".github/workflows/pr.yml"
name: PR Checks
on:
  pull_request:
    paths: ["resources/**"]

jobs:
  validate:
    uses: wardnet/inforge/.github/workflows/validate.yml@v1

  preview:
    needs: validate
    strategy:
      matrix:
        include:
          - environment: prd
            stack_config: inforge.prd.yaml
    uses: wardnet/inforge/.github/workflows/preview.yml@v1
    with:
      environment: ${{ matrix.environment }}
      stack_config: ${{ matrix.stack_config }}
    secrets: inherit
```

```yaml title=".github/workflows/deploy.yml"
name: Deploy
on:
  push:
    branches: [main]
    paths: ["resources/**"]

jobs:
  deploy:
    strategy:
      matrix:
        include:
          - environment: prd
            stack_config: inforge.prd.yaml
    uses: wardnet/inforge/.github/workflows/deploy.yml@v1
    with:
      environment: ${{ matrix.environment }}
      stack_config: ${{ matrix.stack_config }}
    secrets: inherit
```

```yaml title=".github/workflows/reconcile.yml"
name: Reconcile
on:
  schedule:
    - cron: "0 4 * * *"
  workflow_dispatch:

jobs:
  reconcile:
    uses: wardnet/inforge/.github/workflows/reconcile.yml@v1
    secrets: inherit
```

## Required repository secrets

Set these secrets in your repository (Settings → Secrets → Actions):

| Secret | Description | Required for |
|--------|-------------|-------------|
| `HCLOUD_TOKEN` | Hetzner Cloud API token | Compute, Network |
| `CLOUDFLARE_API_TOKEN` | Cloudflare API token | DNS |
| `NEON_API_KEY` | Neon API key | Database |
| `INFISICAL_CLIENT_ID` | Infisical client ID | Secrets |
| `INFISICAL_CLIENT_SECRET` | Infisical client secret | Secrets |

## Bootstrap permissions

For VMs with secret values, the deploy workflow needs OIDC permission to call the
escrow service:

```yaml
permissions:
  id-token: write
  contents: read
```

This is set automatically in the `deploy.yml` reusable workflow.
