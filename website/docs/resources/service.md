---
sidebar_position: 6
---

# Service

A **Service** resource defines an application hosted on a Compute VM. inforge provisions
the host-side scaffolding (folder + systemd unit); the service repo deploys code separately
via `inforge releases` from its own CI workflow.

A service's definition lives in a folder under `regional/service/<name>/`:

```
regional/service/api/
  manifest.yaml        # required — routing, identity, ingress, host
  environment.yaml     # optional — runtime env-var contract (source DSL strings)
```

## Schema

`manifest.yaml`:

```yaml
name: api                # required
container: bridge        # required
host: bridge             # required — name of the Compute resource that hosts this service
type: raw                # required — delivery type
user: wardnet            # required — no-login system user the service runs as
pki: wardnet-mesh        # required — name of the two-tier (mesh) PKI this service is a member of
reload: /bin/kill -HUP $MAINPID  # optional — ExecReload command to apply a renewed mesh leaf without a restart
ingress:                  # optional — typed inbound routes realized on the host's nginx
  - type: tls-termination #   required — tls-termination | forward
    listen: 443           #   required — public port the host accepts traffic on
    target: 8080          #   required (tls-termination) — local port to reverse-proxy to
    vanity:               #   optional — extra public FQDNs beyond the auto-derived <svc>.svc name
      - api.{BASE_DOMAIN}
  - type: forward         #   a second route — raw L4 forward (PROXY protocol)
    listen: 853           #   required — public port
    target: 5353          #   optional (forward) — omit to bind the port directly (firewall only)
```

`environment.yaml` (optional sidecar — env-var name → source DSL string):

```yaml
DATABASE_URL: ref:database/main.connectionUrl   # an output from another resource
CF_TOKEN: env:CLOUDFLARE_API_TOKEN              # a deploy-environment variable
API_KEY: vault:API_KEY                          # a value from the git-encrypted store
LOG_LEVEL: info                                 # a literal (non-secret config) value
```

## Fields

**`manifest.yaml` fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Service name. Also becomes the folder name (`/srv/wardnet/<name>`) and unit name (`wardnet-<name>.service`). |
| `container` | string | Yes | Grouping label. Environment variables are scoped to the container, so services sharing one receive the same values. |
| `host` | string | Yes | **Name** of the Compute resource that hosts this service (e.g. `bridge`). The host must have `instance_count: 1`; a multi-instance host is a validation error. |
| `type` | string | Yes | Delivery type. Currently only `raw` (SSH-push) is supported. `container` is reserved. |
| `user` | string | Yes | No-login system user the service runs as. inforge emits `User=<name>` in the systemd unit and creates the account via SSH on first deploy; the bootstrapper drops privilege to it before exec. |
| `pki` | string | Yes | Name of the **two-tier (mesh) PKI** in `pki.enc.yaml` this service is a leaf member of. `inforge validate` checks it names an existing two-tier PKI with an intermediate for every scope the service deploys under (a global service → `global`; a regional service → every region). See [`inforge pki`](/cli/pki). |
| `reload` | string | No | `ExecReload=` command the service uses to apply a renewed mesh leaf **without a restart** (e.g. `/bin/kill -HUP $MAINPID`, `nginx -s reload`). When set, the per-service renewal timer reloads the unit; when absent, it restarts (a brief interruption). The leaf/key/bundle paths are in the `MTLS_LEAF_CERT_PATH` / `MTLS_LEAF_KEY_PATH` / `MTLS_TRUST_BUNDLE_PATH` env vars. |
| `ingress` | array | No | Typed inbound routes (`tls-termination` / `forward`) fronted by nginx. Each entry binds a public `listen` port; the service binds `127.0.0.1:<target>` behind nginx. See [Ingress](#ingress) below. |

**`environment.yaml` sidecar:**

An optional flat map of `ENV_VAR_NAME: <source>` entries declaring the runtime environment variables
inforge injects when the service starts. If absent the service has no environment variables. See
[Environment](#environment) below.

:::note No `provider` field
A service has **no `provider`** — it is host-managed, not realized by a cloud provider. The secrets
provider a service's environment variables are written to is selected per region by the `infisical`
block in that region's `providers:` in `regions.yaml`, not declared on the service.
:::

## Folder layout

```
regional/service/<name>/
  manifest.yaml       # service identity, host, type, user, ingress
  environment.yaml    # optional — env-var name → source DSL
```

## Delivery types

### `raw`

The service code is delivered as a gzip payload pushed via SSH. inforge creates:

- `/srv/wardnet/<name>/` — service working directory
- `wardnet-<name>.service` — systemd unit (inforge-managed)

The service must provide a `run` executable at the top level of its payload.

### `container` (reserved)

Pull-based container deployment. Not yet implemented.

## Provisioned on-host files

For a service named `api` hosted on `bridge`, `inforge deploy` provisions:

| Path | Description |
|------|-------------|
| `/srv/wardnet/api/` | Service folder (root-owned, world-readable; the service user gets r-x) |
| `/etc/systemd/system/wardnet-api.service` | systemd unit (managed by inforge) |

Provisioning runs over SSH as the host's [`deploy_user`](./compute) — so a service's host **must**
declare one (validation fails otherwise). The unit is written, `daemon-reload`ed, and **enabled**, but
**not started**: its `ExecStart=<folder>/run` does not exist until code is released. After the first
deploy the unit exists but fails to start until [code lands](#releasing-code) — expected.

## Releasing code

`inforge deploy` provisions the scaffolding above; `inforge releases` then delivers the payload
and starts the service. Release resolves the deploy target
(host DNS, folder, unit, SSH user) live from the Pulumi stack — no descriptor file is committed —
then SSHes in as the host's deploy user, extracts the payload into the folder, and
`systemctl restart`s the unit (the first restart is the service's first real start). See the
[service release starter](/github-actions/overview#service-release-optional) for the full setup.

## Service user

Every service must declare a `user`. `inforge deploy`:

1. Emits `User=<name>` in the inforge-managed systemd unit so the service process runs as that account.
2. Creates the account with `useradd --system --shell /usr/sbin/nologin <name>` when provisioning the
   unit (idempotent).

The user is a no-login system account, **distinct from the host's `deploy_user`** (the account inforge
connects as over SSH). It is the account `inforge-bootstrap` drops privilege to before exec, so it is
required for every service — with or without secrets.

## Environment

A service's runtime environment variables are declared in the `environment.yaml` sidecar — a flat
map of `ENV_VAR_NAME: <source>` entries. Each source is a small DSL string that says *where the
value comes from*, not the value itself:

```yaml title="regional/service/api/environment.yaml"
DATABASE_URL: ref:database/main.connectionUrl   # an output from another resource
SERVER_IP: ref:compute/bridge.publicIp
CF_TOKEN: env:CLOUDFLARE_API_TOKEN              # a deploy-environment variable
API_KEY: vault:API_KEY                          # a value from the git-encrypted store
LOG_LEVEL: info                                 # a literal (non-secret config) value
```

The source kinds are:

| Source | Form | Where the value comes from |
|--------|------|----------------------------|
| **ref** | `ref:<database\|compute>/<name>.<output>` | A runtime output of another resource (e.g. a DB connection URL). |
| **env** | `env:<VAR>` | A variable in the **deploy process environment** — e.g. a CI secret mapped to an env var in your workflow. Unset/empty fails the deploy loudly. |
| **vault** | `vault:<KEY>` | A value held **age-encrypted in git** in the env's committed store, keyed by `(container, KEY)`. Managed with the [`inforge secret`](/cli/secret) CLI. |
| **literal** | any other string | A verbatim inline value. **Plaintext in git — non-secret config only.** |

Environment variables are **container-scoped**: every service sharing a `container` receives the
same set. At deploy, inforge resolves every entry (regardless of source kind), writes the value to
the secrets provider under the service's scoped path, and `inforge-bootstrap` injects it as an env
var at start. The [`vault:`](/cli/secret) and full delivery mechanics are covered in
[Secrets](./secrets).

:::warning Literals are not secrets
A literal value (any string without a `ref:`/`env:`/`vault:` prefix) is committed **in plaintext**. Use
it for non-secret per-service config only; use `vault:`, `env:` or `ref:` for anything sensitive.
:::

Env-var names in the reserved `INFORGE_DEPLOYMENT_*` namespace are rejected — see
[Runtime environment](#runtime-environment).

## Ingress

The optional `ingress` field is a **list** of typed inbound routes. A host with any ingress runs
**nginx as its sole public entry point** — there is no separate resource to declare; nginx is installed
and configured automatically wherever a service has ingress, and the service binds `127.0.0.1:<target>`
behind it. Each entry binds a public `listen` port and is one of two types: **`tls-termination`** (nginx
terminates ACME TLS and reverse-proxies to the local port) or **`forward`** (nginx forwards the raw L4
connection to the local port with the PROXY protocol). A service may carry several entries — e.g.
terminate TLS on `443` *and* forward a second protocol on `853`.

Each entry's fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | Yes | `tls-termination` or `forward`. |
| `listen` | int | Yes | Public port (1–65535) nginx accepts traffic on. **No default.** Must differ from `target`. |
| `target` | int | Yes | Loopback port the service listens on; nginx reverse-proxies/forwards to `127.0.0.1:<target>`. Must differ from `listen` (nginx occupies the public port on all interfaces, loopback included). |
| `vanity` | array | No | Extra public FQDNs a `tls-termination` entry serves, **in addition to** the auto-derived `<svc>.svc` name (see below). Invalid on a `forward` (no SNI). |

### Types

- **`tls-termination`**: nginx owns an ACME certificate for each of the entry's FQDNs (the auto-derived
  `<svc>.svc` name plus any `vanity`), terminates TLS on `listen`, and reverse-proxies HTTP to
  `localhost:<target>`. **Several services may share one `listen` port** — nginx demuxes by SNI
  (`server_name`).
- **`forward`**: nginx forwards the raw L4 stream on `listen` to `127.0.0.1:<target>` with
  `proxy_protocol on` so the backend learns the real client address; the **backend owns its own TLS**.
  A `forward` port is **single-service-exclusive** (it cannot be SNI-demuxed).

No host-level resource is needed — nginx realization is driven entirely by ingress presence. ACME owns
`:80` for HTTP-01 challenges, so a `forward` on `:80` cannot coexist with a `tls-termination` on the
same host.

:::tip Raw public ports
`ingress` always goes through nginx. To open a **raw** public port with no proxy (no TLS, no remap, no
PROXY protocol), declare it on the [Compute firewall](./compute#firewall-rules) instead — not as an
ingress entry.
:::

### Hostnames, DNS and certificates

A service's public domain is **derived automatically** from its name:
`<service>.svc.<env>.<slug>.<baseDomain>` (e.g. `api.svc.prd.use1.wardnet.network`). inforge creates
the DNS A-record for it (pointing at the host) on the region's
[DNS authority](../configuration/regions-yaml#dns) — there is no hand-written DNS resource.

To serve additional public names on a `tls-termination` entry, list them under `vanity`:

- a **bare token** (no dot or `{…}`) is env+region-scoped: `api` → `api.<env>.<slug>.<baseDomain>`;
- a value with a **dot or placeholder** is a literal/template FQDN: `key-broker.{BASE_DOMAIN}` →
  `key-broker.<baseDomain>`, or `key-broker.inforge.wardnet.network` used as-is;
- available placeholders: `{BASE_DOMAIN}`, `{ENV}`, `{REGION_SLUG}`.

Each `tls-termination` FQDN (auto + vanity) gets a DNS record and is listed in the service's ACME
certificate (`server_name`). A `forward` entry gets the `<svc>.svc` DNS record but no certificate (the
backend owns TLS).

:::caution Vanity and multiple regions
The auto `<svc>.svc` name embeds the region slug, so it is unique per region. A **region-independent**
vanity (a literal FQDN, or one using only `{BASE_DOMAIN}`) does not — if the service deploys to more
than one region, inforge creates the same DNS name in each region pointing at a different host IP.
Scope such a vanity per region with `{REGION_SLUG}` (or a bare token, which is auto-scoped) unless you
intend the round-robin.
:::

## Example

```yaml title="regional/service/bridge/manifest.yaml"
name: bridge
container: bridge
host: bridge
type: raw
user: bridge
ingress:
  - type: tls-termination # ACME TLS on :443 for bridge.svc.<env>.<slug>.<base> + the vanity names
    listen: 443
    target: 8080
    vanity:
      - key-broker.{BASE_DOMAIN}
      - key-broker.inforge.wardnet.network
  - type: forward         # raw L4 forward of :853 to a local backend (PROXY protocol)
    listen: 853
    target: 5353
```

```yaml title="regional/service/bridge/environment.yaml"
DATABASE_URL: ref:database/main.connectionUrl
SESSION_KEY: vault:SESSION_KEY
```

## Runtime environment

Every service is started by `inforge-bootstrap` (the systemd `ExecStart`), which builds the process
environment from a minimal base (`PATH`, `HOME`, `USER`, `LOGNAME`), then injects:

- the service's **environment variables** (from `environment.yaml`; see [Secrets](./secrets)), and
- the **deployment context** as `INFORGE_DEPLOYMENT_*` variables — derived, non-secret values describing
  where the service is running. These are present for **every** service, including secret-less ones.

| Variable | Example | Meaning |
|----------|---------|---------|
| `INFORGE_DEPLOYMENT_REGION` | `us-east-1` | Abstract region the service is deployed to. |
| `INFORGE_DEPLOYMENT_REGION_SLUG` | `use1` | Region slug used in resource names and FQDNs. |
| `INFORGE_DEPLOYMENT_ENVIRONMENT` | `prd` | Environment name (the `resources/<env>/` directory). |
| `INFORGE_DEPLOYMENT_BASE_DOMAIN` | `wardnet.network` | The environment's base domain. |
| `INFORGE_DEPLOYMENT_NAMESPACE` | `prd.use1.bridge` | `<env>.<slug>.<service>` — a unique per-deployment identifier. |
| `INFORGE_DEPLOYMENT_FQDN` | `bridge.svc.prd.use1.wardnet.network` | The service's auto-derived public FQDN (`<service>.svc.<env>.<slug>.<base>`). |

The `INFORGE_DEPLOYMENT_*` names are reserved — do not map a secret to one of them.
