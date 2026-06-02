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

## Example

```yaml title="resources/prd/us-east-1/service/api.yaml"
name: api
container: bridge
host: bridge-01
type: raw
user: wardnet
```
