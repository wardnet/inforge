---
sidebar_position: 4
---

# inforge matrix

Compute the set of environments that have changes between two git refs. Used by CI to build
the GitHub Actions job matrix for preview/deploy workflows.

## Usage

```bash
inforge matrix --base <ref> [--head <ref>]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--base` | (required) | Base git ref (e.g. `origin/main`, a commit SHA) |
| `--head` | `HEAD` | Head git ref |

## Output

JSON array of objects with `environment` and `stack_config` fields:

```json
[
  {"environment": "prd", "stack_config": "inforge.prd.yaml"},
  {"environment": "dev", "stack_config": "inforge.dev.yaml"}
]
```

If no environments have changed, the output is `[]`.

## How it works

`matrix` diffs `<base>...<head>` (using `git diff --name-only`) and looks for paths matching
`resources/<env>/...`. Each unique `<env>` directory that appears in the diff becomes an entry
in the output.

## Example

```bash
# Which environments changed on this PR?
inforge matrix --base origin/main --head HEAD

# Used in a GitHub Actions workflow
ENVS=$(inforge matrix --base "${{ github.event.pull_request.base.sha }}" --head "${{ github.sha }}")
echo "environments=${ENVS}" >> "$GITHUB_OUTPUT"
```

## Usage in workflows

The `preview`, `deploy`, and `reconcile` reusable workflows call `inforge matrix` to build
the job matrix automatically. See [GitHub Actions: Overview](/github-actions/overview).
