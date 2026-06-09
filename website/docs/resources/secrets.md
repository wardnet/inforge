---
sidebar_position: 5
---

# Secrets

A **Secrets** resource declares a set of secret values for a container. inforge writes them to the
secrets provider under each consuming service's scoped path; the service fetches them at runtime (see
[How secrets reach a service](#how-secrets-reach-a-service) below). Secret values are never baked into
an artifact.

## Schema

```yaml
name: bridge-secrets     # required
container: bridge        # required
provider: infisical      # required
secrets:
  db_url:
    source: ref:database/main.connectionUrl   # from another resource's output
  api_key:
    source: gha:API_KEY                        # from a GitHub Actions secret
  log_level:
    source: static:info                        # a literal (non-secret config) value
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name. |
| `container` | string | Yes | Grouping label. |
| `provider` | string | Yes | Must be `infisical`. |
| `secrets` | map | Yes | Map of secret name → source DSL entry. |

## Source DSL

Each secret entry has a `source` field with one of these forms:

### `ref:<type>/<name>.<output>`

References a runtime output from another resource:

```yaml
source: ref:database/main.connectionUrl
```

The parts are:
- `database` — resource type
- `main` — resource name
- `connectionUrl` — output field name

Supported resource types: `database`, `compute`.

#### Referencing a global resource

A `global/` prefix on the referenced name targets the [global slice](../concepts/global-resources) —
a database or compute that deploys once, region-less, instead of per region:

```yaml
source: ref:database/global/shared.connectionUrl
source: ref:compute/global/edge-01.publicIp
```

This is the **one allowed cross-region reference**: a regional secret may read a global database or
compute output. The reference resolves against the global slice regardless of which region the
consuming service runs in. Referencing a global resource from `service.host` or `compute.network` is
**rejected** — see [Global resources](../concepts/global-resources) for the full rules.

### `gha:<NAME>`

References a GitHub Actions secret:

```yaml
source: gha:MY_SECRET_NAME
```

The GitHub Actions secret `MY_SECRET_NAME` is injected as a secret value. It must be
set as a secret in the repository or environment.

### `static:<value>` (alias `value:<value>`)

A literal value authored inline — useful for non-secret per-service configuration delivered through the
same env-var mechanism as secrets:

```yaml
source: static:info
source: value:https://api.example.com/v1   # the alias; value is taken verbatim
```

The text after the prefix is used **verbatim** (any characters, including `:` and `/`), and must be
non-empty.

:::warning Not for real secrets
A `static:`/`value:` value is committed **in plaintext** in the resource file (it lives in git). Use it
for non-secret configuration only; use [`gha:`](#ghaname) or [`ref:`](#reftypenameoutput) for anything
sensitive.
:::

## How secrets reach a service

inforge does not bake secret values into any artifact. At deploy time, for each service whose
container declares secrets, inforge:

1. Writes the container's secrets to the secrets provider under the service's scoped path
   (`/<service>/infra`).
2. Mints a **per-service machine identity**, scoped read-only to that service's path.
3. Writes two files onto the host under `/etc/wardnet/services/<service>/`:
   - `descriptor.yaml` — a secret-free document with the provider coordinates and an env-var → vault-key
     mapping.
   - `credential.age` — the machine-identity credential, age-encrypted to the host's own SSH host key
     (inforge encrypts it in memory to the host key it reads over SSH; the plaintext never lands on disk).

At service start, `inforge-bootstrap` (the systemd `ExecStart` for every service) decrypts the
credential with the host key, logs in to the provider, fetches the secrets, injects them as environment
variables, drops privilege to the service's `user`, and execs the real binary. Secret *values* live
only in the service process's environment — never on disk, in the journal, or in argv. A service whose
container declares no secrets gets a descriptor with no provider and starts with no fetch at all.

## Example

```yaml title="resources/prd/secrets/bridge-01.yaml"
name: bridge-secrets
container: bridge
provider: infisical
secrets:
  database_url:
    source: ref:database/main.connectionUrl
  stripe_key:
    source: gha:STRIPE_SECRET_KEY
```

## Provider requirements

The Infisical provider is installed via `inforge plugins install`. It requires:

- `INFISICAL_CLIENT_ID` and `INFISICAL_CLIENT_SECRET` environment variables
- `infisical.workspaceId` and `infisical.environment` in the provider config
