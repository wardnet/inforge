---
sidebar_position: 5
---

# Observability dashboards

When [`observability.grafana_url`](/configuration/variables-yaml#observability) is set, `inforge
deploy` manages this environment's Grafana dashboards as code (ADR-0038): every dashboard lives under
an `inforge / <env>` folder and is prefixed by the identity environment, so several inforge
environments can share one Grafana org without colliding. Removing a dashboard from the repo deletes
it from Grafana on the next deploy — the same declare-it, converge-it model as every other resource.

Authentication uses the reserved secret `observability/grafana_token` (a Grafana service-account
token); see [`variables.yaml` → observability](/configuration/variables-yaml#observability).

## Built-in dashboards

inforge generates three dashboards from the metrics it owns. Every query is scoped to the environment
by the `deployment_environment_name` label, and each dashboard has a template variable for the
per-node / per-cluster / per-service drill-down.

| Dashboard | Source metrics | Highlights |
|---|---|---|
| **Infrastructure** | host metrics (ADR-0031, `system_*`) | fleet CPU / memory / disk / network, per-node uptime and restarts |
| **Database** | Postgres receiver metrics (ADR-0037, `postgresql_*`) | connections, transaction rate, DB size, checkpoints — per cluster |
| **Service** | wardnet-cloud RED + domain metrics | request rate, 5xx error rate, and latency percentiles per service and route, plus the domain counters (DDNS networks provisioned, active tunnels, tenant tombstone sweeps) |

The **Service** dashboard reads the RED histogram `http_server_request_duration_seconds_*` that every
wardnet-cloud service emits, grouped by the `service_name` label Grafana Cloud promotes from the OTLP
`service.name` resource attribute. Its "Per Service" section filters on a `Service` variable and
breaks traffic down by `http_response_status_code` and `http_route`.

## Custom dashboards

Anything the built-ins don't cover is authored in the Grafana UI and committed to the repo as an
exported dashboard file:

```
resources/<env>/observability/dashboards/
  traffic-drop.json
  billing.yaml
```

Every `*.json`, `*.yaml`, and `*.yml` file in that directory is pushed into the same env folder on
`inforge deploy`. To add one:

1. Build the dashboard in Grafana.
2. Export it — **Share → Export → Save to file**, or copy the JSON model — and save the file under
   `resources/<env>/observability/dashboards/`. Both the raw dashboard model and the API-wrapped
   form (`{"dashboard": {…}, "meta": {…}}`) are accepted; YAML is accepted too.
3. Commit and deploy.

inforge overwrites the dashboard's `uid` with an env-prefixed one (so the same file deployed to two
environments never collides) and leaves the title, panels, and queries untouched. The filename (minus
extension) becomes the dashboard's stable slug — keep it unique within the directory. To scope a
custom dashboard's queries to the environment, filter on `deployment_environment_name` just as the
built-ins do.

:::note
Alert rules and notification routing are managed separately (a later ADR-0038 slice); this page
covers dashboards only.
:::
