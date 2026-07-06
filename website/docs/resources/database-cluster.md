---
sidebar_position: 3.5
---

# Database cluster

A **Database cluster** is a single running PostgreSQL engine — one `initdb`
instance managing several logical [databases](./database). inforge self-hosts it on
a compute host you provision, and its data lives on a single persistent volume so a
host rebuild plus a volume re-attach recovers every database in the cluster.

Despite the name it is **single-instance** — no HA or replication (that is
PostgreSQL's own term for one instance that manages several databases).

A cluster lives in a folder under `regional/database-cluster/<name>/` (or
`global/database-cluster/<name>/`):

```
regional/database-cluster/pg/
  manifest.yaml       # required — the cluster spec
```

## Schema

`manifest.yaml`:

```yaml
name: pg                 # required — resource name
container: core          # required — grouping label
engine: postgresql       # required — must be "postgresql"
host: db                 # required for self-hosted — a compute in the SAME scope
provider: self-hosted    # optional — defaults to self-hosted (ADR-0036)
version: "17"            # optional — engine major (default "17")
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name (unique within the scope). |
| `container` | string | Yes | Grouping label. |
| `engine` | string | Yes | Must be `postgresql`. |
| `host` | string | Conditional | Compute host FK (same scope). **Required** when the provider is `self-hosted`, **forbidden** when a managed provider is used. |
| `provider` | string | No | Defaults to `self-hosted` — inforge installs and runs PostgreSQL on `host`. |
| `version` | string | No | Engine major version, installed from the PGDG apt repo (default `17`). |

## Self-hosted realization

With `provider: self-hosted` (the default), a deploy:

1. installs the version-pinned PostgreSQL server package on `host`;
2. provisions a **persistent data volume** and mounts it — its size is derived as
   the **sum of the cluster's logical database sizes** (`size_gb`), floored at the
   storage provider's minimum (Hetzner volumes start at 10 GB). The whole `PGDATA`
   lives on it, so re-attaching the volume to a rebuilt host recovers the data;
3. `initdb`s the cluster (only if the volume is empty — a populated volume is never
   re-initialized) and starts the postmaster as a systemd unit;
4. creates each logical database and its `NOLOGIN` owner role.

PostgreSQL listens on the host's **private** network only. Port `5432` (and the next
port up per co-located cluster) is opened to the private network CIDR — never the
public internet — so a cluster is reachable only from peers on the same private
network. Because it is unreachable from the deploy machine, per-service role minting
runs **on the host** over local peer auth.

Co-location works both ways: many logical databases share one cluster (one volume),
and many clusters can run on one host (each its own volume, TCP port, and unit).

## Host requirements

The `host` must be a single-instance `vm` compute in the **same scope** that declares
a `deploy_user` (inforge SSHes in to install and manage PostgreSQL) — the same
requirements as a [service](./service) host.

## Example

```yaml title="regional/database-cluster/pg/manifest.yaml"
name: pg
container: core
engine: postgresql
host: db
```
