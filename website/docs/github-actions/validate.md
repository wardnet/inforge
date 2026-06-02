---
sidebar_position: 2
---

# validate.yml

Validates all resource definitions for environments changed on the current PR.

## Usage

```yaml
uses: wardnet/inforge/.github/workflows/validate.yml@v1
with:
  base_ref: main     # optional — git ref to diff against (default: main)
```

## What it does

1. Computes the environment matrix by running `inforge matrix --base <base_ref> --head HEAD`
2. For each changed environment, runs `inforge validate <env>`
3. Posts a PR comment with `✅` / `❌` per environment

## Inputs

| Input | Type | Default | Description |
|-------|------|---------|-------------|
| `base_ref` | string | `main` | Base git ref for changed-environment detection |

## Required permissions

The workflow posts a comment on the PR. The **calling** workflow must declare these
permissions at the **workflow level** — not inside the job — otherwise GitHub blocks
the run at startup when the repository's default token permission is `read`:

```yaml
permissions:
  contents: read
  pull-requests: write
```

## No secrets required

`inforge validate` only reads YAML files — it does not call any cloud APIs.
