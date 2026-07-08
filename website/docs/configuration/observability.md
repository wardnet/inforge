---
sidebar_position: 5
---

# Observability

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

## Alerts

Alert rules are managed per-env alongside dashboards. Set `built_in_alerts: false` in `variables.yaml`
to opt out of the generated ones; author your own in `resources/<env>/observability/alerts.yaml`.

### Built-in alerts

When `built_in_alerts` is on (the default), inforge generates these from the metrics it owns. All are
env-scoped and fire per node / cluster; the Postgres ones only exist when the env has a database
cluster. Thresholds are fixed — to tune, opt out and re-author as custom alerts.

| Alert | Condition | For | Severity |
|---|---|---|---|
| Host CPU Saturated | CPU used > 90% per node | 10m | warning |
| Host Memory Saturated | memory used > 90% per node | 10m | warning |
| Host Disk Saturated | disk used > 90% per node/mount | 5m | critical |
| Host Disk Will Fill | free space projected to hit zero within 24h | 1h | warning |
| Host Rebooted | node rebooted in the last 10m | — | warning |
| Host Metrics Missing | no host reporting (NoData → alerting) | 5m | critical |
| Postgres Connections High | backends > 80% of max per cluster | 10m | warning |
| Postgres High Rollback Ratio | rollbacks > 25% of transactions per cluster | 15m | warning |
| Postgres Metrics Missing | no cluster reporting (NoData → alerting) | 5m | critical |

### Custom alerts

Each alert is a small spec — inforge generates the Grafana rule (query, reduce, threshold):

```yaml title="resources/prd/observability/alerts.yaml"
alerts:
  - name: tenants-signup-drop
    expr: sum(rate(http_server_request_duration_seconds_count{http_route="/v1/tenants"}[10m]))
    condition: "< 0.01"          # operators: > < >= <=
    for: 15m
    severity: warning            # critical | warning | info
    profile: billing             # omit → default_profile
    summary: "Tenant signups stalled ({{ $value }}/s)"
    labels: { team: billing }
```

`expr` is any PromQL; `condition` is the threshold applied to each series' latest value (so a
multi-series `expr` fires per series, and `{{ $labels.x }}` / `{{ $value }}` in `summary` resolve
per firing series). Scope queries to the env with `deployment_environment_name` as the built-ins do.

## Notifications

Alerts route through **contact points** (reusable named destinations) and **profiles** (per-team
routing tables), authored in `resources/<env>/observability/notifications.yaml`:

```yaml title="resources/prd/observability/notifications.yaml"
contact_points:
  pagerduty-high: { pagerduty: { key_secret: pagerduty_key, severity: critical } }
  pagerduty-low:  { pagerduty: { key_secret: pagerduty_key, severity: warning } }
  pd-billing:     { pagerduty: { key_secret: pd_billing_key } }
  infra-email:    { email: { addresses: [infra@wardnet.io] } }
  slack-alerts:   { webhook: { url: https://hooks.slack.com/… } }

profiles:
  prod:                          # the default team (variables.yaml default_profile: prod)
    critical: { contact_point: pagerduty-high }
    warning:  { contact_point: pagerduty-low }
    info:     { contact_point: infra-email }
  billing:                       # a per-team profile
    critical: { contact_point: pd-billing }
    warning:  { contact_point: pd-billing }
  staging:
    warning:  { muted: true }    # explicitly silence — inforge omits the alert
```

Routing per alert: `profile` (or `default_profile`) → `profiles[profile][severity]` → the named
contact point, applied on the rule itself (Grafana per-rule notification settings). There is **no**
org-level notification policy — each env owns only its own env-prefixed rules and contact points.

- A profile **must** route every `(profile, severity)` an alert can resolve to — a critical alert
  can never be silently unrouted. Use `muted: true` to say "don't notify" explicitly; that route's
  alerts are omitted for the env.
- `key_secret` names a [reserved secret](/cli/secret) under the observability namespace — e.g.
  `inforge secret set prd observability pagerduty_key --reserved`. Several contact points may share
  one key.
- The `default_profile` must define at least `critical` and `warning`, because the built-in alerts
  use those severities. `inforge validate` enforces all of this.
