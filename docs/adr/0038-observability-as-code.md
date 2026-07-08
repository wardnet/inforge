---
status: accepted
date: 2026-07-08
---

# Observability as code (Grafana dashboards, alerts, notifications)

ADR-0030/0031/0037 make inforge the source of the fleet's telemetry: host metrics,
DB metrics, and the resource-attribute identity that app telemetry (wardnet-cloud)
stamps too. What consumes that telemetry — Grafana **dashboards**, **alert rules**,
and **notification routing** — has so far been built by hand against the Grafana API.
That works but lives nowhere: it is not reproducible, not reviewable, not applied to a
fresh env (staging, ephemeral), and not recoverable if the Grafana org is rebuilt.
inforge owns the metrics, so it should own the views and alerts over them, declared in
the repo and delivered on `inforge deploy` — the same "declare it, converge it" model
as every other resource.

## Decisions

- **Dashboards and alert rules are Pulumi resources via the `pulumiverse/grafana`
  provider — not an imperative API push.** inforge is a Pulumi tool; everything else
  (VMs, DNS, DB roles) is a declarative Pulumi resource, and the provider gives
  state, drift detection, `inforge preview`, and — the load-bearing part — **automatic
  cleanup**: remove a dashboard/alert from the repo and it is deleted, with no
  hand-rolled reconcile-and-delete to get subtly wrong. An imperative push
  (`meshBaseline` shape) was considered and rejected for these: its only real
  precedent-justification (secret material that must not enter Pulumi state) does not
  apply to dashboards. The provider resources are created **once at the top of
  `program.Run`, outside the per-scope loop** (they are org-global, not per-region),
  gated on config — absent `observability.grafana_url` it is a no-op.

- **Everything is prefixed by the identity env.** One Grafana org backs every inforge
  env (prd, staging, ephemeral), so dashboard/alert/folder UIDs are prefixed with the
  identity env (the slug, ADR-0028) to keep per-env stacks from colliding. Metrics
  already carry `deployment_environment_name` (= identity env) as the query-time
  discriminator, so a dashboard scopes to its env by both.

- **Built-in dashboards, generated from the metrics inforge owns.** `internal/grafanadash`
  (pure, Pulumi-free, like `internal/otelcol`/`internal/nginx`) renders **Infrastructure**,
  **Database**, and **Service** dashboards from `system_*`, `postgresql_*`, and
  wardnet-cloud's RED + domain metrics — parameterized by env and by which clusters
  exist. A new env gets real monitoring on first deploy, opt-in via config. Custom
  dashboards are **Grafana-exported** JSON/YAML files committed under
  `observability/dashboards/` — authored in the UI, exported, versioned; no bespoke DSL.

- **Alerts are a simplified spec, not raw Grafana rule JSON.** An alert is
  `name / expr / condition / for / severity / profile / summary / labels`; inforge
  generates the Grafana RuleGroup (the `data[]` query + threshold expression). Alerts
  are **multi-dimensional**: the query's own `by (...)` grouping makes one rule fire per
  node / service / region, and those series labels flow to the notification
  (`{{ $labels.service_name }}`), exactly as the node alerts already carry
  `{{ $labels.instance }}`. `condition` supports `> < >= <=` (a traffic-drop alert is a
  `<` threshold). inforge ships the standard host/DB alerts plus optional per-service
  http error-rate / latency; a traffic-drop alert is inherently custom (only the author
  knows a service's baseline).

- **Notification routing is a user-defined "profile", decoupled from env lifecycle.** A
  **profile** maps `severity → contact point + urgency` (e.g. `prod`: critical→PagerDuty
  high, warning→PagerDuty low; `staging`: warning→muted; `silent`: all→none). Each alert
  carries a profile (the env sets a default — prd→`prod` — overridable per alert), and
  the generated notification policy routes on `profile` then `severity`. This inverts the
  org-singleton problem: the policy is *derived* from profiles, not hand-owned. Because
  the Grafana notification policy and contact points are **one org-wide resource** that a
  per-env stack cannot co-own, they are applied by a **dedicated, env-decoupled
  `inforge observability sync`** (its own org-scoped stack) — so destroying prd never
  tears down the routing staging relies on.

- **Config follows the reserved-secret precedent (ADR-0031).**
  `observability.grafana_url` is a non-secret field in `variables.yaml`; the Grafana
  service-account token and contact-point secrets (the PagerDuty integration key) are
  **reserved secrets** (`observability/grafana_token`, …) in `secrets.enc.yaml`, written
  `--reserved` and read via `decryptReservedSecret` — never a service-container secret
  (`.agents/rules/reserved-secrets-live-outside-container-namespace.md`).

- **Per-cluster / per-database / per-region drill-down needs collector-side label
  promotion.** `db.cluster.name`, the receiver's `postgresql.database.name`, and the
  inforge `region` slug are stamped as **resource** attributes (ADR-0037/0030), so in the
  OTel→Prometheus translation they land on `target_info`, not as queryable series labels.
  A `transform` processor on each pg pipeline promotes them to **datapoint** attributes so
  they become real labels — in-repo and reproducible, rather than an out-of-band Grafana
  Cloud promotion setting.

## Consequences

- Adds the `pulumiverse/grafana` provider dependency (the fifth provider, alongside
  hetzner/cloudflare/command/random); it stays `CGO_ENABLED=0`.
- Org notifications (contact points + policy) live outside any env's stack, owned by
  `inforge observability sync`; per-env deploys own only their prefixed dashboards +
  alert rules. Ephemeral envs get prefixed dashboards/alerts that Pulumi auto-deletes on
  `ephemeral down`.
- Lands in four slices: ① provider + config + built-in Infrastructure/Database
  dashboards; ② built-in Service dashboard + custom (exported) dashboards; ③ alert spec
  + built-ins + profiles + `observability sync`; ④ collector-side label promotion
  (`db.cluster.name`, `postgresql.database.name`, `region`).
- The label-promotion slice re-touches ADR-0037's `internal/otelcol` and must be
  live-verified on a Postgres host (a resource-vs-datapoint attribute mistake is
  invisible until queried in Grafana).
