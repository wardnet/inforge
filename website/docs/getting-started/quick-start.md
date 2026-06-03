---
sidebar_position: 3
---

# Quick Start

This guide walks you through setting up a new inforge project from scratch.

## 1. Create the project structure

```
my-infra/
├── inforge.yaml              # project config
├── inforge.prd.yaml          # stack config for prd environment
└── resources/
    └── prd/
        ├── variables.yaml    # regions, providers, SSH config
        ├── us-east-1/
        │   ├── network/
        │   │   └── ingress-01.yaml
        │   ├── compute/
        │   │   └── bridge-01.yaml
        │   └── dns/
        │       └── bridge-01.yaml
        └── services/
            └── api-01.yaml
```

## 2. Write `inforge.yaml`

```yaml
name: my-infra
backend:
  type: file
  url: file://.pulumi
broker:
  url: https://inforge-key-broker.workers.dev
```

See [inforge.yaml reference](/configuration/inforge-yaml) for all backend types (file, git-branch, s3, r2).

## 3. Write `inforge.prd.yaml`

```yaml
config:
  environment: prd
  inforge:broker_url: https://inforge-key-broker.workers.dev
  inforge:oidc_token: ""   # set at deploy time via OIDC
  hcloud:token: ""         # set via HCLOUD_TOKEN env var
  cloudflare:apiToken: ""  # set via CLOUDFLARE_API_TOKEN env var
```

## 4. Write `resources/prd/variables.yaml`

```yaml
base_domain: example.com
regions:
  - name: us-east-1
    providers:
      hetzner:
        location: ash
      cloudflare:
        zoneId: ""
ssh:
  authorizedKeys: "ssh-ed25519 AAAA... user@host"
  deployPublicKey: "ssh-ed25519 AAAA... deploy@host"
providers:
  hetzner:
    token: ""   # set via HCLOUD_TOKEN
  cloudflare:
    apiToken: "" # set via CLOUDFLARE_API_TOKEN
```

## 5. Write a compute resource

```yaml title="resources/prd/us-east-1/compute/bridge-01.yaml"
name: bridge
instance: 1
container: bridge
provider: hetzner
network: ingress-01
size: SMALL
image: ubuntu-24.04
cloud_init: bridge-01.cloud-init.sh
```

## 6. Validate

```bash
inforge validate prd
```

## 7. Preview

```bash
inforge preview --stack prd --stack-config inforge.prd.yaml
```

## 8. Deploy

```bash
inforge deploy --stack prd --stack-config inforge.prd.yaml --yes
```

## Next steps

- Read the [Resource Reference](/resources/network) for all resource types and fields
- Set up [GitHub Actions](/github-actions/overview) for automated preview and deploy
- Learn how [secret bootstrapping works](/concepts/bootstrap-key-broker)
