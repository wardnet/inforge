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
| `--ssh-key` | `$INFORGE_DEPLOY_KEY` | SSH deploy key used by the post-deploy [mesh baseline](#mesh-baseline) trigger (only needed when the env has mesh services) |
| `--config` / `-c` | `./inforge.yaml` | Path to the project config file |
| `--dir` / `-d` | `./resources` | Path to the resources directory |

## Interactive confirmation

Without `--yes`, inforge prompts:

```
Deploy stack "prd"? Type 'yes' to confirm:
```

Use `--yes` in CI or scripted contexts: when stdin is not a terminal the prompt cannot be answered,
and `deploy` fails rather than exiting 0 having applied nothing.

## Secret delivery

For each service whose container declares secrets, `deploy` resolves every `environment.yaml` entry
(plus any `grants:` outputs) into one plaintext map, age-encrypts it directly to the host's own SSH
key, and writes a secret-free `descriptor.yaml` plus that encrypted `secrets.age` onto the host over
SSH. `inforge-agent` decrypts `secrets.age` locally at boot and injects the values as environment
variables; no secret value is baked into any artifact and there is no runtime fetch from any backend.
See [Secrets → How secrets reach a service](../resources/secrets#how-secrets-reach-a-service).

## Mesh baseline

When the environment has mesh services (any service with `pki:`), `deploy` runs one more step after a
successful `up`: it mints the environment's mesh leaf material (the same core as
[`inforge pki renew`](./pki)) and SSHes each mesh host to push its updated `leaf.age` directly, then
unconditionally reload-or-restarts the mesh proxy — so the per-host proxies pick up real leaves
immediately instead of serving their self-signed placeholder certificates. The step needs
`INFORGE_SECRETS_KEY` (already required for secret delivery) and the deploy SSH key (`--ssh-key` /
`INFORGE_DEPLOY_KEY`). A failed push is reported per host; rerun `inforge pki renew` to retry. See
[How a renewed leaf reaches a running host](./pki#how-a-renewed-leaf-reaches-a-running-host).

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
