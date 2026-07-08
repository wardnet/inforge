---
status: accepted
date: 2026-07-08
---

# PostgreSQL database metrics

Host metrics (ADR-0031) tell us about the VM — CPU, memory, disk — but nothing about
the Postgres running on it (ADR-0036): connections, commits/rollbacks, cache hit
ratio, replication lag, table bloat, index usage. inforge owns the self-hosted
database, so collecting its metrics is inforge's responsibility, exactly as for host
metrics. We add the OpenTelemetry Collector Contrib **`postgresql` receiver** to the
per-host collector we already run, on cluster hosts, exporting to the same Grafana
Cloud OTLP endpoint tagged with the same resource-attribute set — so DB metrics
correlate with host and app telemetry on `host.id`.

## Decisions

- **The `postgresql` receiver in the existing per-host collector**, not
  `postgres_exporter` + a Prometheus receiver, nor the `sqlquery` receiver. The
  receiver ships in the contrib `.deb` we already install (ADR-0031) — no new
  binary/dialect — and emits OTel-native metrics with the resource-attribute model
  the fleet uses. `sqlquery` would be hand-written SQL to reinvent the same metrics.
- **One receiver + metrics pipeline per cluster**, scraping the LOCAL instance over
  `127.0.0.1:<port>` (the collector runs on the cluster host). The `pg_hba`
  `host all all 127.0.0.1/32 scram-sha-256` line (ADR-0036) already admits the
  loopback TCP connection; TLS is disabled for the loopback hop.
- **A dedicated `pg_monitor` monitoring role per cluster, minted DIRECTLY — not via a
  grant.** The collector reads only statistics views, so it gets Postgres's built-in
  `pg_monitor` role (`pg_read_all_stats`) plus `CONNECT` on each scraped database, and
  nothing else — no schema/table privileges, no user-data access. This bends
  `.agents/rules/db-credentials-flow-only-through-grants.md`: the monitoring role is an
  **inforge-internal observability credential** (there is no service granting itself
  access — the collector is inforge's own), analogous to the reserved OTLP credential
  and the mesh leaves, which are likewise minted/delivered outside the grant path. So
  it is minted like a grant role (`random.RandomPassword`, on-host peer auth via
  `postgres.MintMonitorRoleScript`) but wired straight into the observability pass. Its
  password is written `0600` owned by the collector user and read from the config via
  the `${file:…}` provider — the exact pattern the OTLP credential uses (ADR-0031), so
  the secret never appears in the config text.
- **Resource attributes give three filter levels, and are multi-node-ready.** DB metrics
  carry a DISTINCT `service.name = "wardnet-db-metrics"` (Prometheus `job`),
  `service.instance.id = host.id` (per-node, as ADR-0031 stamps for host metrics), and
  `db.cluster.name = <cluster>` — so metrics filter **global** (the job) → **per-cluster**
  (`db.cluster.name`) → **per-node** (`service.instance.id`); per-database drill-down is
  free (the receiver stamps `postgresql.database.name`). This is deliberately
  forward-compatible: when a cluster spans multiple hosts, each node's collector scrapes
  its local instance and stamps the shared `db.cluster.name` with its own
  `service.instance.id`, so "inside the cluster, per node" works with no design change
  (today N=1). The shared ADR-0030 identity (`host.id`, `cloud.*`, region, env) is
  stamped too, so DB metrics correlate with host metrics on `host.id`.
- **All databases are scraped by default; a database opts out with `metrics: false`.** A
  new optional `*bool` on the database spec (mirroring `backup.enabled`): omitted/true =
  scraped, false = excluded (no per-database metrics, no `CONNECT` for the monitor role).
  A cluster whose databases all opt out gets no receiver.

## Consequences

- Extends ADR-0031 (same collector, install, credential pattern) and ADR-0030 (same
  resource identity). No distribution change — the receiver is in the pinned `.deb`.
- The observability pass now depends on the Postgres cluster being up (it mints the
  monitor role over local peer auth), so it runs after `provisionDatabaseClusters` and
  `DependsOn` the cluster's per-host tail; the collector restart waits on every mint so
  it never starts a receiver whose role does not yet exist.
- One documented exception to the grant-only rule, scoped narrowly to inforge's own
  observability role — recorded here so a future reader knows why a DB role lives
  outside grants.
