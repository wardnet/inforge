---
sidebar_position: 4
---

# Database

A **Database** is a single *logical* PostgreSQL database inside a
[database cluster](./database-cluster) — the `CREATE DATABASE app` unit, owned by a
role, that a service connects to. Many databases can share one cluster.

The engine, host, and version live on the **cluster**; a database only names its
cluster, its logical name, its owner, an optional declared size, and an optional
backup policy.

A database lives in a folder under `regional/database/<name>/` (or
`global/database/<name>/`):

```
regional/database/main/
  manifest.yaml       # required — the database spec
```

## Schema

`manifest.yaml`:

```yaml
name: main               # required — resource name
container: bridge        # required — grouping label
cluster: pg              # required — database-cluster FK (SAME scope)
database: app            # required — the logical database name
owner: app               # required — PostgreSQL role that owns it (created NOLOGIN)
size_gb: 5               # optional — declared size intent (default 0)
backup:                  # optional — per-database backup policy
  enabled: true          #   default true (set false to opt a throwaway db out)
  interval: 24h          #   default 24h
  keep: 7                #   default 7
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name (unique within the scope). |
| `container` | string | Yes | Grouping label. |
| `cluster` | string | Yes | The [database cluster](./database-cluster) this database belongs to (same scope). |
| `database` | string | Yes | The logical PostgreSQL database name. |
| `owner` | string | Yes | The owner role (created `NOLOGIN` — nothing logs in as it; per-service login roles are minted by grants). |
| `size_gb` | integer | No | Declared size intent (default `0`). PostgreSQL has no per-database quota, so this sums into the cluster's derived [volume size](./database-cluster#self-hosted-realization) rather than being enforced. |
| `backup` | object | No | Backup policy — `enabled` (default `true`), `interval` (default `24h`), `keep` (default `7`). |

## Access

A database is a **Grantable**: a service reaches it only through a [grant](./service#grants), never a
`ref:` (`ref:database/*` is rejected so an owner credential is never handed to a consumer). A grant
mints a **scoped per-service role** with only the privileges the permission level allows — `ro`
(`CONNECT`/`USAGE`/`SELECT`) or `rw` (read/write plus `CREATE ON SCHEMA public`) — and materializes the
connection fields as env vars:

```yaml title="in regional/service/api/manifest.yaml"
grants:
  - resource: database/main         # the logical database name
    permission: rw
    outputs:
      DATABASE_URL: "{URL}"          # {USER} {PASSWORD} {HOST} {PORT} {DBNAME} {URL} are published
```

Consumer manifests are unchanged by the cluster/database split — a grant still targets the logical
`database/<name>`. See [Service — Grants](./service#grants) for the full field set and mechanics.

## Example

```yaml title="regional/database/main/manifest.yaml"
name: main
container: bridge
cluster: pg
database: app
owner: app
```
