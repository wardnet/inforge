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

## Access

A database is a **Grantable**: a service reaches it only through a [grant](./service#grants), never a
`ref:` (`ref:database/*` is rejected so an owner credential is never handed to a consumer). A grant
mints a **scoped per-service role** with only the privileges the permission level allows — `ro`
(`CONNECT`/`USAGE`/`SELECT`) or `rw` (read/write plus `CREATE ON SCHEMA public`) — and materializes the
connection fields as env vars:

```yaml title="in regional/service/api/manifest.yaml"
grants:
  - resource: database/main
    permission: rw
    outputs:
      DATABASE_URL: "{URL}"     # {USER} {PASSWORD} {HOST} {PORT} {DBNAME} {URL} are published
```

See [Service — Grants](./service#grants) for the full field set and mechanics.

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
