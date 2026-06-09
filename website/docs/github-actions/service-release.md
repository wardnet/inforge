---
sidebar_position: 6
---

# service-release.yml

Releases a service to a provisioned VM. Called by service repos after their build step. It runs
[`inforge releases push`](/cli/releases) (upload the artifact to the R2 release store, keyed by
commit SHA) followed by `inforge releases deploy` (deliver that SHA to the host and record it in the
per-environment manifest).

There is no deploy descriptor file to commit: the target host, folder, and systemd unit are resolved
live from the infrastructure Pulumi stack at release time. A `concurrency` group serialises releases
per service+environment.

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
| `sha` | string | No | triggering commit | Artifact SHA to push + deploy |
| `dry_run` | boolean | No | `false` | Resolve the target and verify the artifact without delivering |

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
2. **push**: packages `artifact_path` and uploads it to the release store as `<service>/<SHA>.tar.gz`, then prunes old artifacts (keeping live and recent ones)
3. **deploy**: resolves the deploy target (host DNS, folder, systemd unit) live from the Pulumi stack, downloads `<SHA>.tar.gz`, and SCPs it onto the host
4. Extracts the payload into the service folder and restarts the inforge-managed systemd unit
5. Records `host → SHA` in `manifest.<env>.yaml` in the release store

The release store bucket and retention are configured in the platform repo's `inforge.yaml`
`artifacts:` block — see [`inforge releases`](/cli/releases#configuration).

## Payload requirements

The artifact directory must contain a `run` executable at its top level:

```
dist/
├── run          # the service executable (must be named "run")
└── config.yaml  # any supporting files
```
