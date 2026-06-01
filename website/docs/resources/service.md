---
sidebar_position: 6
---

# Service

A **Service** resource defines an application hosted on a Compute VM. inforge provisions
the host-side scaffolding (folder + systemd unit); the service repo deploys code separately
via the `deploy-raw` reusable workflow.

## Schema

```yaml
name: api                # required
container: bridge        # required
provider: ""             # optional — services have no provider (host-managed)
host: bridge-01          # required — specKey of the Compute VM that hosts this service
type: raw                # required — delivery type
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Service name. Also becomes the folder name (`/srv/wardnet/<name>`) and unit name (`wardnet-<name>.service`). |
| `container` | string | Yes | Grouping label. |
| `provider` | string | No | Unused for services; omit or leave empty. |
| `host` | string | Yes | specKey of the Compute VM that hosts this service. |
| `type` | string | Yes | Delivery type. Currently only `raw` (SSH-push) is supported. `container` is reserved. |

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

## Deploy descriptor

After a successful deploy, inforge writes a deploy descriptor to `deploy/<env>.yaml`:

```yaml
environment: prd
targets:
  - service: api
    host_dns: bridge.use1.example.com
    folder: /srv/wardnet/api
    unit: wardnet-api.service
```

The `deploy-raw` workflow uses this to push code updates.

## Example

```yaml title="resources/prd/services/api-01.yaml"
name: api
container: bridge
host: bridge-01
type: raw
```
