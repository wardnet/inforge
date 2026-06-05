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
user: wardnet            # optional — no-login system user the service runs as (raw only)
ingress:                 # optional — expose this service via the host's TLS terminator
  hostname: api          #   required — host label, env-scoped into an FQDN
  port: 8080             #   required — local port the service listens on
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Service name. Also becomes the folder name (`/srv/wardnet/<name>`) and unit name (`wardnet-<name>.service`). |
| `container` | string | Yes | Grouping label. |
| `provider` | string | No | Unused for services; omit or leave empty. |
| `host` | string | Yes | specKey of the Compute VM that hosts this service. |
| `type` | string | Yes | Delivery type. Currently only `raw` (SSH-push) is supported. `container` is reserved. |
| `user` | string | No | No-login system user the service runs as (`raw` only). When set, inforge emits `User=<name>` in the systemd unit and creates the user via SSH on first deploy. When absent, no user is created. |
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

For a service named `api` hosted on `bridge-01`:

| Path | Description |
|------|-------------|
| `/srv/wardnet/api/` | Service payload directory |
| `/etc/systemd/system/wardnet-api.service` | systemd unit (managed by inforge) |
| `/etc/wardnet/manifest.yaml` | Service manifest (may be SOPS-encrypted) |
| `/etc/wardnet/bootstrap.yaml` | One-time bootstrap doc (deleted after first boot) |

## Releasing code

The `service-release` workflow resolves the deploy target (host DNS, folder, unit) live
from the Pulumi stack at release time. No descriptor file needs to be committed anywhere.
See [`service-release.yml`](/github-actions/service-release) for the full setup.

## Service user

When `user` is set, inforge:

1. Emits `User=<name>` in the inforge-managed systemd unit so the service process runs as that account.
2. Runs `useradd --system --shell /usr/sbin/nologin <name>` via SSH on first deploy (idempotent — safe to re-run).

The user is a no-login system account. This field is only meaningful for `type: raw` services;
container services manage their user inside the image.

## Ingress

The optional `ingress` block exposes a service for inbound traffic through its host's
[TLS termination](./tls-termination) resource. Declaring `ingress` means "terminate TLS for this
host and reverse-proxy to its local port": the terminator writes one vhost per service that does
exactly that, with an ACME-issued certificate. There is no non-TLS ingress — a service that wants
raw inbound traffic opens the port on the host firewall instead (see below).

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `hostname` | string | Yes | Host label for the service. The env-scoped FQDN it resolves to (matching the form `<hostname>.<env>.<slug>.<baseDomain>`) is derived at deploy time. |
| `port` | int | Yes | Local port (1–65535) the service listens on; the terminator reverse-proxies to it. |

A service that declares `ingress` **must** have a `tls-termination` resource on its host — validation
fails otherwise. A service that instead wants to receive raw inbound traffic should open the port on
the [Compute](./compute) host firewall and declare no `ingress`.

## Example

```yaml title="resources/prd/us-east-1/service/api.yaml"
name: api
container: bridge
host: bridge-01
type: raw
user: wardnet
```
