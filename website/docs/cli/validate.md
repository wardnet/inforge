---
sidebar_position: 1
---

# inforge validate

Validate all resource definitions for an environment against their JSON schemas.

## Usage

```bash
inforge validate <env> [flags]
```

## Arguments

| Argument | Description |
|----------|-------------|
| `env` | Environment name (e.g. `prd`, `dev`). Resolves to `resources/<env>/`. |

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dir` / `-d` | `./resources` | Path to the resources directory |
| `--config` / `-c` | `./inforge.yaml` | Path to the project config file |

## Output

Reports each invalid file with its path and schema errors:

```
resources/prd/us-east-1/compute/bridge-01.yaml: provider is required
resources/prd/us-east-1/network/ingress-01.yaml: valid

validation failed
```

Exits `0` on success, non-zero on any validation error.

## Example

```bash
inforge validate prd
inforge validate dev --dir ./infra/resources
```

## When to run

Always run `inforge validate` before opening a PR. The CI workflow calls it automatically
via the `validate` reusable workflow.
