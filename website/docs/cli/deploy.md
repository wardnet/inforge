---
sidebar_position: 3
---

# inforge deploy

Deploy (apply) infrastructure changes for a stack using the Pulumi Automation API.

## Usage

```bash
inforge deploy --stack <env> [flags]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--stack` / `-s` | (required) | Stack name / environment (e.g. `prd`) |
| `--stack-config` | `inforge.<stack>.yaml` | Path to the stack config file |
| `--yes` / `-y` | `false` | Auto-approve without interactive prompt |
| `--output` / `-o` | `""` (human) | Output format: `""` for human-readable, `json` for structured JSON |
| `--allow-multiple` | `false` | Allow running when multiple environments have changes |
| `--config` / `-c` | `./inforge.yaml` | Path to the project config file |
| `--dir` / `-d` | `./resources` | Path to the resources directory |

## Interactive confirmation

Without `--yes`, inforge prompts:

```
Deploy stack "prd"? Type 'yes' to confirm:
```

Use `--yes` in CI or scripted contexts.

## Bootstrap integration

When a manifest contains secret values, `deploy` automatically:

1. Mints a fresh age key K and one-time token T
2. Fetches a GitHub Actions OIDC token (from `inforge:oidc_token` stack config)
3. Calls `PUT /token` on the escrow service
4. Writes `bootstrap.yaml` to the VM via cloud-init

The workflow must have `id-token: write` permission for this to work.

## State management

After a successful deploy, inforge:

- Writes the deploy descriptor to `deploy/<env>.yaml`
- If using `git-branch` backend: commits and pushes updated Pulumi state to the state branch

## JSON output

```json
{
  "environment": "prd",
  "summary": {
    "create": 2,
    "update": 1,
    "delete": 0,
    "same": 4
  }
}
```

## Examples

```bash
# Interactive deploy
inforge deploy --stack prd --stack-config inforge.prd.yaml

# CI deploy (auto-approve, JSON output)
inforge deploy --stack prd --stack-config inforge.prd.yaml --yes --output json
```
