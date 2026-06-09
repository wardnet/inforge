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

Provider config lives in `resources/<env>/regions.yaml` — under each region's
`providers:` block, and/or the sibling `global:` block for the global slice (see
ADR-0011). Infisical credentials are usually identical across regions:

```yaml
# resources/prd/regions.yaml
regions:
  us-east-1:
    slug: use1
    providers:
      infisical:
        clientId: ${INFISICAL_CLIENT_ID}        # universal-auth client ID
        clientSecret: ${INFISICAL_CLIENT_SECRET} # universal-auth client secret
        organizationId: ""                       # optional — see "Organization ID" below
```

### Organization ID

Per-service machine identities are provisioned under an Infisical organization.
By default inforge reads the organization ID from the `organizationId` claim in
the universal-auth access token's JWT. Some Infisical deployments — Infisical
Cloud in particular — issue tokens that do **not** carry that claim, which
surfaces at deploy time as:

```
infisical:resources:InfisicalIdentity ... error: infisical: no organizationId in JWT claims
```

Set `organizationId` on the region's `infisical` provider block to fix this — it
takes precedence over the JWT claim. A service in a region reads that region's
block; a service in the global slice reads `global.providers.infisical`. Find
your organization ID in the Infisical dashboard URL
(`https://app.infisical.com/organization/<organizationId>/...`).

## Required env vars

| Variable | Description |
|----------|-------------|
| `INFISICAL_CLIENT_ID` | Infisical machine identity client ID |
| `INFISICAL_CLIENT_SECRET` | Infisical machine identity client secret |
