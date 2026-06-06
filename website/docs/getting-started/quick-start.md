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
        │   │   └── ingress.yaml
        │   ├── compute/
        │   │   └── bridge.yaml
        │   └── dns/
        │       └── bridge.yaml
        └── services/
            └── api-01.yaml
```

## 2. Write `inforge.yaml`

```yaml
name: my-infra
backend:
  type: file
  url: file://.pulumi
```

See [inforge.yaml reference](/configuration/inforge-yaml) for all backend types (file, git-branch, s3, r2).

## 3. Write `inforge.prd.yaml`

```yaml
config:
  environment: prd
  hcloud:token: ""         # set via HCLOUD_TOKEN env var
  cloudflare:apiToken: ""  # set via CLOUDFLARE_API_TOKEN env var
```

## 4. Write `resources/prd/variables.yaml`

```yaml
base_domain: example.com
regions:
  - name: eu-central-1
providers:
  hetzner:
    apiToken: ${HCLOUD_TOKEN}
    regions:
      eu-central-1:
        location: nbg1
        network_zone: eu-central
        serverTypes: {SMALL: cx23, MEDIUM: cx33, LARGE: cx43}
        images: {ubuntu-24.04: ubuntu-24.04}
  cloudflare:
    apiToken: ${CLOUDFLARE_API_TOKEN}
    zoneId: ""
ssh:
  authorizedKeys: "ssh-ed25519 AAAA... user@host"
  deployPublicKey: "ssh-ed25519 AAAA... deploy@host"
```

## 5. Write a compute resource

```yaml title="resources/prd/eu-central-1/compute/bridge.yaml"
name: bridge
container: bridge
provider: hetzner
network: ingress
size: SMALL
image: ubuntu-24.04
cloud_init: bridge.cloud-init.sh
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
- Learn how [secrets reach a service](/resources/secrets#how-secrets-reach-a-service)
