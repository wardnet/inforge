---
sidebar_position: 3
---

# variables.yaml

Per-environment variables file at `resources/<env>/variables.yaml`. It holds the base domain and SSH
config for the environment. Which regions deploy and all provider config (credentials + per-region
realizations) live in [`regions.yaml`](/configuration/regions-yaml).

## Schema

```yaml
base_domain: example.com     # required — base domain for VM DNS names

ssh:                         # required when using compute
  authorizedKeys: "ssh-ed25519 AAAA..."    # user access SSH public key(s)
  deployPublicKey: "ssh-ed25519 AAAA..."   # deploy user's public key

observability:               # optional — Grafana Cloud integration
  otlp_endpoint: https://otlp-gateway-....grafana.net/otlp   # host/DB metrics collector
  grafana_url: https://myorg.grafana.net                     # dashboards + alerts target
```

## Fields

### `base_domain`

The root domain for DNS names. A host's domain is assembled as
`<compute>.vm.<env>.<region-slug>.<base_domain>` (e.g. `bridge.vm.prd.use1.wardnet.network`), and a
service's as `<service>.svc.<env>.<region-slug>.<base_domain>`.

### `ssh`

SSH keys placed on every provisioned VM:

- `authorizedKeys` — added to the VM's authorized_keys for human (admin) access.
- `deployPublicKey` — the SSH public key installed for the deploy user. When a compute
  resource declares a [`deploy_user`](/resources/compute#deploy-user), inforge provisions
  that account at VM-init time and installs this key into its `authorized_keys`. The
  username itself is set per-compute in `deploy_user.name`; the key material lives here
  so that rotating it only requires updating `variables.yaml` and re-running `inforge deploy`.

### `observability`

Optional Grafana Cloud integration. Both fields are non-secret URLs; their credentials
are [reserved secrets](/cli/secret) in `secrets.enc.yaml`, never committed here.

- `otlp_endpoint` — the OTLP/HTTP base URL for the host VM-metrics and Postgres-metrics
  collector (ADR-0031/0037). When set, inforge installs the collector on every VM; its
  Basic-auth credential is the reserved secret `observability/otlp_auth`
  (`inforge secret set <env> observability otlp_auth --reserved`). Empty ⇒ no collector.
- `grafana_url` — the base URL of the Grafana instance inforge pushes the built-in
  dashboards (and, in later slices, alerts) to (ADR-0038). When set, `inforge deploy`
  realizes this env's [dashboards](/configuration/observability) under an
  `inforge / <env>` folder, prefixed by env so multiple environments can share one
  Grafana org. Its service-account token is the
  reserved secret `observability/grafana_token`
  (`inforge secret set <env> observability grafana_token --reserved`). A `grafana_url`
  set with no token fails the deploy. Empty ⇒ no dashboards are managed.
- `built_in_dashboards` — whether inforge manages the generated Infrastructure/Database/
  Service [dashboards](/configuration/observability) for this env. Default `true`; set
  `false` to opt out (custom dashboards are unaffected).
- `built_in_alerts` — whether inforge manages the generated [alert rules](/configuration/observability#alerts)
  for this env. Default `true`; set `false` to opt out (custom alerts are unaffected).
- `default_profile` — the notification [profile](/configuration/observability#notifications)
  (from `observability/notifications.yaml`) that built-in alerts and any alert omitting
  `profile:` route through. Required once alerts are managed.

## Example

```yaml title="resources/prd/variables.yaml"
base_domain: example.com
ssh:
  authorizedKeys: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... user@laptop"
  deployPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... deploy@ci"
observability:
  otlp_endpoint: https://otlp-gateway-prod-eu-west-2.grafana.net/otlp
  grafana_url: https://wardnet.grafana.net
  default_profile: prod          # notification profile for built-in + un-profiled alerts
  # built_in_dashboards: false   # opt out of the generated dashboards
  # built_in_alerts: false       # opt out of the generated alert rules
```
