---
sidebar_position: 5
---

# inforge release

Releases a service artifact to a provisioned VM. Reads the `deployments/` directory in the
current repo to find the platform repo and per-service config, resolves the deploy target
live from the Pulumi stack, and SSH-delivers the artifact.

## Usage

```
inforge release --service <name> --env <env> [flags]
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--service` | `-s` | required | Service name — must match `deployments/<service>.yaml` |
| `--env` | `-e` | required | Target environment (e.g. `qa`, `prd`) |
| `--deploy-dir` | | `./deployments` | Path to the deployments directory |
| `--stack-config` | | `inforge.<env>.yaml` | Path to the infra stack config file |
| `--ssh-key` | | `$INFORGE_DEPLOY_KEY` | Path to the SSH deploy key |
| `--dry-run` | | `false` | Resolve and print the target without delivering |

## Examples

Release the `api` service to `qa`:

```sh
inforge release --service api --env qa
```

Dry-run to inspect the resolved target without delivering:

```sh
inforge release --service api --env prd --dry-run
```

## Deployments directory

`inforge release` reads two files from the deployments directory:

```yaml title="deployments/inforge.yaml"
platform: wardnet/infra   # platform repo running inforge for your infrastructure
services:
  - api
```

```yaml title="deployments/api.yaml"
environments:
  qa:
    artifact_path: dist
  prd:
    artifact_path: dist
    health_check: /healthz   # optional: HTTP path checked after unit restart
```

## SSH key

The deploy key path is resolved in order:

1. `--ssh-key` flag
2. `INFORGE_DEPLOY_KEY` environment variable

The key must correspond to `ssh.deployPublicKey` set in `variables.yaml` of the platform repo.

## Related

- [`service-release.yml`](/github-actions/service-release) — the GHA wrapper that calls this command
