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
    └── prd/                  # defined once, instantiated into every region
        ├── variables.yaml    # base_domain + SSH config
        ├── regions.yaml      # regions + per-region provider config
        ├── network/
        │   └── ingress.yaml
        ├── compute/
        │   └── bridge.yaml
        ├── dns/
        │   └── bridge.yaml
        └── service/
            └── api.yaml
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
ssh:
  authorizedKeys: "ssh-ed25519 AAAA... user@host"
  deployPublicKey: "ssh-ed25519 AAAA... deploy@host"
```

## 4b. Write `resources/prd/regions.yaml`

```yaml
regions:
  eu-central-1:
    slug: euc1
    providers:
      hetzner:
        apiToken: ${HCLOUD_TOKEN}
        location: nbg1
        network_zone: eu-central
        serverTypes: {SMALL: cx23, MEDIUM: cx33, LARGE: cx43}
        images: {ubuntu-24.04: ubuntu-24.04}
      cloudflare:
        apiToken: ${CLOUDFLARE_API_TOKEN}
        zoneId: ""
```

## 5. Write a compute resource

```yaml title="resources/prd/compute/bridge.yaml"
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
