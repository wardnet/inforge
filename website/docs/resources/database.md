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

A database resource lives in a folder under `regional/database/<name>/`:

```
regional/database/main/
  manifest.yaml       # required — the database spec
```

## Schema

`manifest.yaml`:

```yaml
name: main               # required
container: bridge        # required
provider: neon           # optional — inherits from inforge.yaml providers.database.postgresql
engine: postgresql       # required — must be "postgresql"
branch: main             # optional — Neon branch name (default "main")
database: app            # required — database name within the branch
owner: app               # required — PostgreSQL role that owns the database
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name. |
| `container` | string | Yes | Grouping label. |
| `provider` | string | No | Must be `neon`. Inherits from `inforge.yaml` `providers.database.<engine>` if omitted. |
| `engine` | string | Yes | Must be `postgresql`. |
| `branch` | string | No | Neon branch name (default `main`). |
| `database` | string | Yes | PostgreSQL database name. |
| `owner` | string | Yes | PostgreSQL role that owns the database. |

## Outputs

| Output | Description |
|--------|-------------|
| `connectionUrl` | PostgreSQL connection string |

This output can be referenced from a service's `environment.yaml`:

```yaml
# in regional/service/api/environment.yaml
DATABASE_URL: ref:database/main.connectionUrl
```

## Example

```yaml title="regional/database/main/manifest.yaml"
name: main
container: bridge
engine: postgresql
database: app
owner: app
```

## Provider requirements

The Neon provider is installed via `inforge plugins install`. It requires:

- `NEON_API_KEY` environment variable
- `neon.projectId` in the provider config
