---
sidebar_position: 4
---

# Infisical

The Infisical provider implements **Secrets** resources using
[Infisical](https://infisical.com) for secret management.

:::note Status
Core secret materialisation is available. The provider resolves `ref:` and `gha:` sources,
encrypts them into the VM manifest, and triggers bootstrap.
:::

## Installation

The Infisical provider is a separate binary (`pulumi-resource-infisical`) installed via:

```bash
inforge plugins install
```

## Resources

| Resource | Status |
|----------|--------|
| Secrets | Available |

## Configuration

```yaml
providers:
  infisical:
    clientId: ""      # set via INFISICAL_CLIENT_ID
    clientSecret: ""  # set via INFISICAL_CLIENT_SECRET
    workspaceId: ""   # your Infisical workspace/project ID
    environment: prd  # Infisical environment slug
```

## Required env vars

| Variable | Description |
|----------|-------------|
| `INFISICAL_CLIENT_ID` | Infisical machine identity client ID |
| `INFISICAL_CLIENT_SECRET` | Infisical machine identity client secret |
