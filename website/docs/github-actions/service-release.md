---
sidebar_position: 6
---

# service-release.yml

Releases a service artifact to a provisioned VM. Called by service repos after their build step.

Unlike the old `deploy-raw` pattern, there is no deploy descriptor file to commit.
`service-release` resolves the target host, folder, and systemd unit live from the
infrastructure Pulumi stack at release time.

## Usage

In your **service** repo:

```yaml title=".github/workflows/release.yml"
name: Release
on:
  push:
    branches: [main]

jobs:
  release:
    uses: wardnet/inforge/.github/workflows/service-release.yml@v1
    with:
      service: api
      environment: qa
    secrets:
      deploy_ssh_key: ${{ secrets.DEPLOY_SSH_KEY }}
```

For a tag-triggered production release:

```yaml title=".github/workflows/release.yml"
name: Release
on:
  push:
    branches: [main]
  push:
    tags: ["v*"]

jobs:
  release-qa:
    if: github.ref_type == 'branch'
    uses: wardnet/inforge/.github/workflows/service-release.yml@v1
    with:
      service: api
      environment: qa
    secrets:
      deploy_ssh_key: ${{ secrets.DEPLOY_SSH_KEY }}

  release-prd:
    if: github.ref_type == 'tag'
    uses: wardnet/inforge/.github/workflows/service-release.yml@v1
    with:
      service: api
      environment: prd
    secrets:
      deploy_ssh_key: ${{ secrets.DEPLOY_SSH_KEY }}
```

## Inputs

| Input | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `service` | string | Yes | — | Service name — must match `deployments/<service>.yaml` |
| `environment` | string | Yes | — | Target environment (e.g. `qa`, `prd`) |
| `stack_config` | string | No | `inforge.<env>.yaml` | Path to the infra stack config file |
| `deploy_dir` | string | No | `./deployments` | Path to the deployments directory |
| `dry_run` | boolean | No | `false` | Resolve and print the target without delivering |

## Secrets

| Secret | Description |
|--------|-------------|
| `deploy_ssh_key` | Private SSH key matching `ssh.deployPublicKey` in `variables.yaml` |

## Deployments directory layout

The service repo must contain a `deployments/` directory:

```
deployments/
├── inforge.yaml          # platform repo + service list
└── api.yaml              # per-service, per-environment artifact config
```

```yaml title="deployments/inforge.yaml"
platform: wardnet/infra   # the repo running inforge for your infrastructure
services:
  - api
```

```yaml title="deployments/api.yaml"
environments:
  qa:
    artifact_path: dist
  prd:
    artifact_path: dist
```

`artifact_path` is the local directory whose contents are packaged and delivered to the host.

## What it does

1. Reads `deployments/inforge.yaml` to locate the platform repo and `deployments/<service>.yaml` for the artifact path
2. Resolves the deploy target (host DNS, folder, systemd unit) live from the Pulumi stack output in the platform repo
3. Packages `artifact_path` as a `payload.tgz`
4. Uploads the payload via SCP and extracts it into the service folder
5. Restarts the inforge-managed systemd unit

## Payload requirements

The artifact directory must contain a `run` executable at its top level:

```
dist/
├── run          # the service executable (must be named "run")
└── config.yaml  # any supporting files
```
