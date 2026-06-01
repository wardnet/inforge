---
sidebar_position: 6
---

# deploy-raw.yml

Delivers a service code payload to a provisioned VM via SSH and restarts the inforge-managed
systemd unit. Called by service repos — not infrastructure repos.

## Usage

In your **service** repo:

```yaml title=".github/workflows/deploy.yml"
name: Deploy
on:
  push:
    branches: [main]

jobs:
  deploy:
    uses: wardnet/inforge/.github/workflows/deploy-raw.yml@v1
    with:
      service: api
      environment: prd
      artifact_path: dist
      descriptor_path: deploy/prd.yaml   # checked in to the service repo
    secrets:
      deploy_ssh_key: ${{ secrets.DEPLOY_SSH_KEY }}
```

## Inputs

| Input | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `service` | string | Yes | — | Service name (matches a target in the deploy descriptor) |
| `environment` | string | Yes | — | Target environment (e.g. `prd`) |
| `artifact_path` | string | No | `dist` | Directory whose contents are delivered to the host |
| `descriptor_path` | string | No | `deploy/<environment>.yaml` | Path to the deploy descriptor |

## Secrets

| Secret | Description |
|--------|-------------|
| `deploy_ssh_key` | Private SSH key matching `ssh.deployPublicKey` in `variables.yaml` |

## Deploy descriptor

The deploy descriptor (`deploy/<env>.yaml`) is produced by `inforge deploy` and must be
checked in to the service repo. It tells `deploy-raw` where to push the payload:

```yaml
environment: prd
targets:
  - service: api
    host_dns: bridge.use1.example.com
    folder: /srv/wardnet/api
    unit: wardnet-api.service
```

## What it does

1. Reads the deploy descriptor to find the target host, folder, and systemd unit
2. Packages `artifact_path` as a `payload.tgz`
3. Configures SSH with the deploy key
4. Uploads the payload via SCP to `/tmp/payload.tgz` on the host
5. Extracts the payload into the service folder
6. Restarts the systemd unit

## Payload requirements

The payload directory must contain a `run` executable at its top level. This is the entry
point for the inforge-managed systemd unit:

```
dist/
├── run          # the service executable (must be named "run")
└── config.yaml  # any supporting files
```
