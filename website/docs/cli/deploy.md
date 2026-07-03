---
sidebar_position: 3
---

# inforge deploy

Deploy (apply) infrastructure changes for a stack using the Pulumi Automation API.

## Usage

```bash
inforge deploy <env> [flags]
```

The environment is a required positional argument (e.g. `prd`) — it is the Pulumi stack name.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--stack-config` | `inforge.<env>.yaml` | Path to the stack config file (optional; a missing default file means no extra config) |
| `--yes` / `-y` | `false` | Auto-approve without interactive prompt |
| `--output` / `-o` | `""` (human) | Output format: `""` for human-readable, `json` for structured JSON |
| `--allow-multiple` | `false` | Allow running when multiple environments have changes |
| `--ssh-key` | `$INFORGE_DEPLOY_KEY` | SSH deploy key used by the post-deploy [mesh baseline](#mesh-baseline) trigger (only needed when the env has mesh services) |
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
its own secrets at runtime via `inforge-agent`; no secret value is baked into any artifact. See
[Secrets → How secrets reach a service](../resources/secrets#how-secrets-reach-a-service).

## Mesh baseline

When the environment has mesh services (any service with `pki:`), `deploy` runs one more step after a
successful `up`: it mints the environment's mesh cert material into the secrets provider (the same
core as [`inforge pki renew`](./pki)) and SSHes each mesh host to start its `wardnet-mesh-renew`
oneshot, so the per-host mesh proxies pull real leaves immediately instead of serving their
placeholder certificates until the daily timer. The step needs `INFORGE_SECRETS_KEY` (already required
for secret delivery) and the deploy SSH key (`--ssh-key` / `INFORGE_DEPLOY_KEY`). A failed trigger is
reported per host but the material is already in the provider — those hosts converge on their own
timer. See [How a renewed leaf reaches a running host](./pki#how-a-renewed-leaf-reaches-a-running-host).

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
