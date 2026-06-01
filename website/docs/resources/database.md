---
sidebar_position: 4
---

# Database

A **Database** resource defines a managed PostgreSQL database via the Neon provider.

:::note Status
The Neon provider is implemented via a Pulumi Go provider (`pulumi-resource-neon`).
Core database provisioning is available; advanced features (branching, connection pooling)
are planned for a future release.
:::

## Schema

```yaml
name: main               # required
container: bridge        # required
provider: neon           # required
engine: postgresql       # required — must be "postgresql"
branch: main             # optional — Neon branch name (default "main")
database: app            # required — database name within the branch
role: app                # required — database role/user
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name. |
| `container` | string | Yes | Grouping label. |
| `provider` | string | Yes | Must be `neon`. |
| `engine` | string | Yes | Must be `postgresql`. |
| `branch` | string | No | Neon branch name (default `main`). |
| `database` | string | Yes | PostgreSQL database name. |
| `role` | string | Yes | PostgreSQL role/user for the application. |

## Outputs

| Output | Description |
|--------|-------------|
| `connectionUrl` | PostgreSQL connection string |

This output can be referenced by a Secrets resource:

```yaml
# in secrets/bridge-01.yaml
secrets:
  db_url:
    source: ref:database/main.connectionUrl
```

## Example

```yaml title="resources/prd/us-east-1/database/main-01.yaml"
name: main
container: bridge
provider: neon
engine: postgresql
database: app
role: app
```

## Provider requirements

The Neon provider is installed via `inforge plugins install`. It requires:

- `NEON_API_KEY` environment variable
- `neon.projectId` in the provider config
