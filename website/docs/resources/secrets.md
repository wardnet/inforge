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
    source: ${API_KEY}                          # from an environment variable
  stripe_key:
    source: encrypted                          # from the git-committed encrypted store
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

### `${NAME}`

References an environment variable — the same `${ENV_VAR}` convention used in
`variables.yaml`/`regions.yaml`:

```yaml
source: ${MY_SECRET_NAME}
```

The value is read from the deploy process environment. You inject it however you like — e.g. a CI
secret mapped to an env var in your workflow (`env: { MY_SECRET_NAME: ${{ secrets.MY_SECRET_NAME }} }`).
An unset or empty value fails the deploy loudly rather than materialising an empty secret. The name
must be upper-snake-case (`[A-Z_][A-Z0-9_]*`).

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
for non-secret configuration only; use [`encrypted`](#encrypted), [`${NAME}`](#name) or
[`ref:`](#reftypenameoutput) for anything sensitive.
:::

### `encrypted`

The value lives **age-encrypted in git**, in the environment's committed secret store
(`resources/<env>/secrets.enc.yaml`), keyed by the spec's `container` and the secret's key:

```yaml
source: encrypted
```

The bare token carries no payload — the store entry is addressed by `(container, KEY)`. Values are
written with the [`inforge secret` CLI](/cli/secret) (the only writer of the store) and encrypted to
the store's committed public *recipient*, so **anyone with commit access can add or replace a secret
value without any private key** (and cannot decrypt what they wrote). At deploy, inforge decrypts the values in CI with the master identity from
the `INFORGE_SECRETS_KEY` environment variable and writes the plaintext into the provider under
`infra/<KEY>` — exactly where the other source kinds land. The provider is a *derived cache*: it is
written only by the deploy, never by the CLI, so the provider always reflects the last deployed git
state (see [ADR-0017](https://github.com/wardnet/inforge/blob/main/docs/adr/0017-git-native-encrypted-secret-store.md)).

`encrypted` is the right default for app secrets that should live in git (API keys, signing keys,
tokens). `${NAME}` remains correct for values genuinely external to the deploy.

`inforge validate` fails if a declared `encrypted` secret has no ciphertext entry in the env's store,
so a missing value is caught before any deploy.

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
    source: ${STRIPE_SECRET_KEY}
```

## Provider requirements

The Infisical provider is installed via `inforge plugins install`. It requires:

- `INFISICAL_CLIENT_ID` and `INFISICAL_CLIENT_SECRET` environment variables
- `infisical.workspaceId` and `infisical.environment` in the provider config
