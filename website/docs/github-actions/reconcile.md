---
sidebar_position: 5
---

# reconcile.yml

Detects and corrects infrastructure drift by applying all changed environments on a schedule.

## Usage

```yaml
uses: wardnet/inforge/.github/workflows/reconcile.yml@v1
with:
  base_ref: main    # optional
secrets: inherit
```

Or use the built-in schedule (4 AM UTC daily) by calling the workflow directly:

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

## What it does

1. Computes the matrix of environments with changes since `base_ref`
2. For each environment, runs `inforge deploy --yes`
3. On success: closes any open `infra-drift` GitHub issue
4. On failure: creates an `infra-drift` issue linking to the failed run

## Drift issues

The reconcile workflow manages a GitHub issue labelled `infra-drift`:

- Created when a reconcile fails
- Closed automatically when a subsequent reconcile succeeds
- Links directly to the failed workflow run for quick diagnosis

## Inputs

| Input | Type | Default | Description |
|-------|------|---------|-------------|
| `base_ref` | string | `main` | Base ref for environment matrix detection |
