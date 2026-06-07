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
ingress:                 # optional — expose this service via the host's TLS terminator
  hostname: api          #   required — host label, env-scoped into an FQDN (the SNI)
  port: 8080             #   required — local port traffic is forwarded to
  tls: terminate         #   optional — terminate (default) | passthrough
  catchall: false        #   optional — at most one per host; implies passthrough
  proxy_protocol: ""     #   optional — v1 | v2 (passthrough/catchall only)
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
| `ingress` | object | No | Exposes the service for inbound traffic via the host's [TLS termination](./tls-termination) resource. See [Ingress](#ingress) below. |

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

The optional `ingress` block exposes a service for inbound traffic through its host's
[TLS termination](./tls-termination) resource. The terminator routes by **SNI** (the service's
env-scoped FQDN) and, per service, either terminates TLS or passes it through:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `hostname` | string | Yes | Host label for the service — the SNI matched. The env-scoped FQDN it resolves to (`<hostname>.<env>.<slug>.<baseDomain>`) is derived at deploy time. Unused for matching when `catchall` is set. |
| `port` | int | Yes | Local port (1–65535) traffic is forwarded to. |
| `tls` | string | No | `terminate` (default) or `passthrough`. |
| `catchall` | bool | No | Marks this service as the host's catch-all. **At most one per host.** Implies `passthrough`. |
| `proxy_protocol` | string | No | `v1` or `v2`. Sends the PROXY protocol header to the upstream on passthrough/catch-all routes so the backend learns the real client address. Ignored on terminate routes. |

### Modes

- **`terminate`** (default): the terminator owns an ACME certificate for the FQDN, terminates TLS, and
  reverse-proxies HTTP to `localhost:<port>`. This is the original behavior — existing configs are
  unchanged.
- **`passthrough`**: the raw TLS stream for the FQDN is forwarded by SNI to `localhost:<port>`; the
  **backend owns its own TLS** (its own certificate). Set `proxy_protocol: v2` if the backend needs
  the real client IP.
- **catch-all**: a single service per host whose `ingress.catchall: true` receives every SNI not
  matched by a named route. It is always passthrough; the dispatcher backend reads the SNI from the
  TLS handshake it receives (`proxy_protocol` defaults to `v2`, conveying the client address).

A service that declares `ingress` **must** have a `tls-termination` resource on its host — validation
fails otherwise. A service that instead wants raw inbound traffic (no SNI routing) should open the
port on the [Compute](./compute) host firewall and declare no `ingress`.

## Example

```yaml title="resources/prd/us-east-1/service/api.yaml"
name: api
container: bridge
host: bridge-01
type: raw
user: wardnet
```
