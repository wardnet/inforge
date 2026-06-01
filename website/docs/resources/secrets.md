---
sidebar_position: 5
---

# Secrets

A **Secrets** resource declares a set of secret values to materialise into a VM's manifest.
The presence of any secret value triggers VM bootstrapping (see [Bootstrap & Escrow](../concepts/bootstrap-escrow)).

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
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name. |
| `container` | string | Yes | Grouping label. |
| `provider` | string | Yes | Must be `infisical`. |
| `secrets` | map | Yes | Map of secret name → source DSL entry. |

## Source DSL

Each secret entry has a `source` field with one of two forms:

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

### `gha:<NAME>`

References a GitHub Actions secret:

```yaml
source: gha:MY_SECRET_NAME
```

The GitHub Actions secret `MY_SECRET_NAME` is injected as a secret value. It must be
set as a secret in the repository or environment.

## How secrets are encrypted

When a manifest contains secret values, inforge:

1. Mints a fresh age key K
2. Encrypts the secret fields in the manifest with SOPS/age using K as the recipient
3. Registers K with the escrow under a one-time token T
4. Writes `bootstrap.yaml` to the VM with the escrow URL and T

At first boot, the VM redeems T for K, decrypts the manifest, and re-encrypts to
its own SSH key. K is then discarded.

## Example

```yaml title="resources/prd/us-east-1/secrets/bridge-01.yaml"
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
