---
sidebar_position: 3
---

# Neon

The Neon provider implements **Database** resources using
[Neon serverless PostgreSQL](https://neon.tech).

:::note Status
Core database provisioning is available. Advanced features (branching, read replicas,
connection pooling configuration) are planned.
:::

## Installation

The Neon provider is a separate binary (`pulumi-resource-neon`) installed via:

```bash
inforge plugins install
```

## Resources

| Resource | Status |
|----------|--------|
| Database | Available |

Database resources may omit their `provider:` field when `providers.database.postgresql: neon` is set
in [`inforge.yaml`](/configuration/inforge-yaml#providers). An explicit `provider: neon` on a
resource always takes precedence over the project default.

## Configuration

```yaml
providers:
  neon:
    apiKey: ""      # set via NEON_API_KEY
    projectId: ""   # your Neon project ID
```

## Required env vars

| Variable | Description |
|----------|-------------|
| `NEON_API_KEY` | Neon API key |

## Outputs

| Output | Description |
|--------|-------------|
| `connectionUrl` | PostgreSQL connection string (can be referenced via `ref:database/<name>.connectionUrl`) |
