---
sidebar_position: 6
---

# Service

A **Service** resource defines an application hosted on a Compute VM. inforge provisions
the host-side scaffolding (folder + systemd unit); the service repo deploys code separately
via the `service-release` reusable workflow.

## Schema

```yaml
name: api                # required
container: bridge        # required
provider: ""             # optional — services have no provider (host-managed)
host: bridge-01          # required — specKey of the Compute VM that hosts this service
type: raw                # required — delivery type
user: wardnet            # required — no-login system user the service runs as
ingress:                 # optional — list of inbound routes via the host's TLS terminator
  - port: 8080           #   required — local port traffic is forwarded to
    tls: terminate       #   optional — terminate (default) | passthrough
    vanity:              #   optional — extra public FQDNs beyond the auto-derived <svc>.svc name
      - api.{BASE_DOMAIN}
  - port: 8443           #   a second route — at most one catch-all per service
    catchall: true       #   optional — at most one per host; implies passthrough
    proxy_protocol: v2   #   optional — v1 | v2 (passthrough/catchall only)
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Service name. Also becomes the folder name (`/srv/wardnet/<name>`) and unit name (`wardnet-<name>.service`). |
| `container` | string | Yes | Grouping label. |
| `provider` | string | No | Unused for services; omit or leave empty. |
| `host` | string | Yes | specKey of the Compute VM that hosts this service. |
| `type` | string | Yes | Delivery type. Currently only `raw` (SSH-push) is supported. `container` is reserved. |
| `user` | string | Yes | No-login system user the service runs as. inforge emits `User=<name>` in the systemd unit and creates the account via SSH on first deploy; the bootstrapper drops privilege to it before exec. |
| `ingress` | array | No | List of inbound routes exposed via the host's [TLS termination](./tls-termination) resource. At most one catch-all and at most one non-catch-all entry per service. See [Ingress](#ingress) below. |

## Delivery types

### `raw`

The service code is delivered as a gzip payload pushed via SSH. inforge creates:

- `/srv/wardnet/<name>/` — service working directory
- `wardnet-<name>.service` — systemd unit (inforge-managed)

The service must provide a `run` executable at the top level of its payload.

### `container` (reserved)

Pull-based container deployment. Not yet implemented.

## Provisioned on-host files

For a service named `api` hosted on `bridge-01`, `inforge deploy` provisions:

| Path | Description |
|------|-------------|
| `/srv/wardnet/api/` | Service folder (root-owned, world-readable; the service user gets r-x) |
| `/etc/systemd/system/wardnet-api.service` | systemd unit (managed by inforge) |

Provisioning runs over SSH as the host's [`deploy_user`](./compute) — so a service's host **must**
declare one (validation fails otherwise). The unit is written, `daemon-reload`ed, and **enabled**, but
**not started**: its `ExecStart=<folder>/run` does not exist until code is released. After the first
deploy the unit exists but fails to start until [code lands](#releasing-code) — expected.

## Releasing code

`inforge deploy` provisions the scaffolding above; `inforge release` (the `service-release`
workflow) then delivers the payload and starts the service. Release resolves the deploy target
(host DNS, folder, unit, SSH user) live from the Pulumi stack — no descriptor file is committed —
then SSHes in as the host's deploy user, extracts the payload into the folder, and
`systemctl restart`s the unit (the first restart is the service's first real start). See
[`service-release.yml`](/github-actions/service-release) for the full setup.

## Service user

Every service must declare a `user`. `inforge deploy`:

1. Emits `User=<name>` in the inforge-managed systemd unit so the service process runs as that account.
2. Creates the account with `useradd --system --shell /usr/sbin/nologin <name>` when provisioning the
   unit (idempotent).

The user is a no-login system account, **distinct from the host's `deploy_user`** (the account inforge
connects as over SSH). It is the account `inforge-bootstrap` drops privilege to before exec, so it is
required for every service — with or without secrets.

## Ingress

The optional `ingress` field is a **list** of inbound routes exposed through the host's
[TLS termination](./tls-termination) resource. Each entry routes by **SNI** and either terminates TLS
or passes it through. A single service may carry **at most one catch-all** and **at most one
non-catch-all** entry — the bridge shape (terminate its own SNI, pass everything else through) is one
service with two entries, deployed once.

Each entry's fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `port` | int | Yes | Local port (1–65535) traffic is forwarded to. |
| `tls` | string | No | `terminate` (default) or `passthrough`. |
| `catchall` | bool | No | Marks this entry as the host's catch-all. **At most one per host (and per service).** Implies `passthrough`; has no SNI, so no `vanity`. |
| `vanity` | array | No | Extra public FQDNs a terminate/named route serves, **in addition to** the auto-derived `<svc>.svc` name (see below). Ignored on a catch-all. |
| `proxy_protocol` | string | No | `v1` or `v2`. Sends the PROXY protocol header to the upstream on passthrough/catch-all routes so the backend learns the real client address. Ignored on terminate routes. |

### Hostnames, DNS and certificates

A non-catch-all entry's matched SNI / certificate domain is **derived automatically** from the service
name: `<service>.svc.<env>.<slug>.<baseDomain>` (e.g. `api.svc.prd.use1.wardnet.network`). inforge
creates the DNS A-record for it (pointing at the host) on the region's
[DNS authority](../configuration/regions-yaml#dns-authority) — there is no hand-written DNS resource.

To serve additional public names, list them under `vanity`:

- a **bare token** (no dot or `{…}`) is env+region-scoped: `api` → `api.<env>.<slug>.<baseDomain>`;
- a value with a **dot or placeholder** is a literal/template FQDN: `key-broker.{BASE_DOMAIN}` →
  `key-broker.<baseDomain>`, or `key-broker.inforge.wardnet.network` used as-is;
- available placeholders: `{BASE_DOMAIN}`, `{ENV}`, `{REGION_SLUG}`.

Each terminate FQDN (auto + vanity) gets its own DNS record and its own ACME certificate; a named
passthrough FQDN gets a DNS record but no certificate (the backend owns TLS); a catch-all gets neither.

:::caution Vanity and multiple regions
The auto `<svc>.svc` name embeds the region slug, so it is unique per region. A **region-independent**
vanity (a literal FQDN, or one using only `{BASE_DOMAIN}`) does not — if the service deploys to more
than one region, inforge creates the same DNS name in each region pointing at a different host IP.
Scope such a vanity per region with `{REGION_SLUG}` (or a bare token, which is auto-scoped) unless you
intend the round-robin.
:::

### Modes

- **`terminate`** (default): the terminator owns an ACME certificate for each of the entry's FQDNs,
  terminates TLS, and reverse-proxies HTTP to `localhost:<port>`.
- **`passthrough`**: the raw TLS stream for the entry's FQDNs is forwarded by SNI to `localhost:<port>`;
  the **backend owns its own TLS**. Set `proxy_protocol: v2` if the backend needs the real client IP.
- **catch-all**: an entry with `catchall: true` receives every SNI not matched by a named route. It is
  always passthrough; `proxy_protocol` defaults to `v2` so the dispatcher backend learns the client
  address.

A service that declares `ingress` **must** have a `tls-termination` resource on its host — validation
fails otherwise. A service that instead wants raw inbound traffic (no SNI routing) should open the
port on the [Compute](./compute) host firewall and declare no `ingress`.

## Example

```yaml title="resources/prd/us-east-1/service/bridge.yaml"
name: bridge
container: bridge
host: bridge-01
type: raw
user: bridge
ingress:
  - port: 8080            # terminate: bridge.svc.<env>.<slug>.<base> + the vanity names
    tls: terminate
    vanity:
      - key-broker.{BASE_DOMAIN}
      - key-broker.inforge.wardnet.network
  - port: 8443            # catch-all: every other SNI, passed through to the dispatcher
    catchall: true
```
