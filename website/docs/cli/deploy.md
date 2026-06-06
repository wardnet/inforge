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

## Secret delivery

For each service whose container declares secrets, `deploy` writes the secrets to the provider under
the service's scoped path, mints a per-service machine identity, and writes a secret-free
`descriptor.yaml` plus a host-key-encrypted `credential.age` onto the host over SSH. The service fetches
its own secrets at runtime via `inforge-bootstrap`; no secret value is baked into any artifact. See
[Secrets → How secrets reach a service](../resources/secrets#how-secrets-reach-a-service).

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
