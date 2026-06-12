---
sidebar_position: 5
---

# Secrets

Secrets are **not a standalone resource**. A service declares the runtime values it needs in its
`environment.yaml` sidecar — an `ENV_VAR_NAME: <source>` map — and inforge injects each as an
environment variable when the service starts. This page covers the source DSL, the git-encrypted store
behind `vault:`, and how a resolved value reaches a running service. Secret values are never baked
into an artifact.

```yaml title="regional/service/api/environment.yaml"
DATABASE_URL: ref:database/main.connectionUrl   # an output from another resource
CF_TOKEN: env:CLOUDFLARE_API_TOKEN              # a deploy-environment variable
API_KEY: vault:API_KEY                          # a value from the git-encrypted store
LOG_LEVEL: info                                 # a literal (non-secret config) value
```

Environment variables are **container-scoped**: every service sharing a `container` receives the same
set of values. The [`inforge secret`](/cli/secret) CLI keys the encrypted store by `(container, KEY)`
and takes a service name only as a handle onto its container.

## Source DSL

Each entry in `environment.yaml` is a source DSL string naming **where the value comes from** — never
the value itself. There are four kinds:

### `ref:<database|compute>/<name>.<output>`

A runtime output of another resource:

```yaml
DATABASE_URL: ref:database/main.connectionUrl
SERVER_IP: ref:compute/bridge.publicIp
```

The parts are the resource **type** (`database` or `compute`), the resource **name**, and the
**output** field. A `global/` prefix on the name targets the [global slice](../concepts/global-resources):

```yaml
DATABASE_URL: ref:database/global/shared.connectionUrl
EDGE_IP: ref:compute/global/edge-01.publicIp
```

This is the **one allowed cross-region reference**: a regional service secret may read a global
database or compute output, resolving against the global slice regardless of the service's region. See
[Global resources](../concepts/global-resources) for the full rules.

### `env:<VAR>`

A value read from the **deploy process environment** — the same mechanism `variables.yaml` /
`regions.yaml` use for provider credentials:

```yaml
CF_TOKEN: env:CLOUDFLARE_API_TOKEN
```

You inject `CLOUDFLARE_API_TOKEN` however you like — typically a CI secret mapped to an env var in your
workflow (`env: { CLOUDFLARE_API_TOKEN: ${{ secrets.CLOUDFLARE_API_TOKEN }} }`). An unset or empty
value fails the deploy loudly rather than materialising an empty secret. `env:` is correct for values
genuinely external to the deploy; for app secrets that should live in git, prefer `vault:`.

### `vault:<KEY>`

A value held **age-encrypted in git**, in the environment's committed secret store
(`resources/<env>/secrets.enc.yaml`), keyed by the service's `container` and `<KEY>`:

```yaml
API_KEY: vault:API_KEY                 # store key == env-var name
DATABASE_URL: vault:PROD_DB_URL        # store key decoupled from the env-var name
```

The store key is independent of the env-var name, so `DATABASE_URL: vault:PROD_DB_URL` is valid. Values
are written with the [`inforge secret`](/cli/secret) CLI — the only writer of the store — and encrypted
to the store's committed public **recipient**, so **anyone with commit access can add or replace a
secret value without any private key** (and cannot decrypt what they wrote). At deploy, inforge decrypts
the values in CI with the master identity from the `INFORGE_SECRETS_KEY` environment variable and writes
the plaintext into the provider, exactly where the other source kinds land.

`vault:` is the right default for app secrets that should live in git (API keys, signing keys, tokens).
The provider is a *derived cache*: it is written only by the deploy, never by the CLI, so it always
reflects the last deployed git state (see
[ADR-0017](https://github.com/wardnet/inforge/blob/main/docs/adr/0017-git-native-encrypted-secret-store.md)).
`inforge validate` fails if a `vault:` secret has no matching ciphertext entry in the env's store, so a
missing value is caught before any deploy.

See **[Creating a secret](#creating-a-secret)** below for the full workflow.

### literal

Any string **without** a `ref:`/`env:`/`vault:` prefix is used verbatim — useful for non-secret
per-service configuration delivered through the same env-var mechanism:

```yaml
LOG_LEVEL: info
API_BASE: https://api.example.com/v1
```

:::warning Not for real secrets
A literal value is committed **in plaintext** (it lives in git). Use it for non-secret configuration
only; use `vault:`, `env:` or `ref:` for anything sensitive.
:::

## Creating a secret

A `vault:` value lives in the env's committed, age-encrypted store. The full lifecycle:

1. **Initialise the store once per environment** (see [Initialising the store](#initialising-the-store)):

   ```sh
   inforge secret init prd
   ```

2. **Declare the secret on the consuming service** in its `environment.yaml`:

   ```yaml
   API_KEY: vault:API_KEY
   ```

3. **Write the value** with the [`inforge secret`](/cli/secret) CLI (the `<service>` argument resolves
   to its container; the value is read from stdin, or `--generate` mints a random one):

   ```sh
   pbpaste | inforge secret set prd api API_KEY     # value from stdin
   inforge secret set prd api SESSION_KEY --generate # random 32-byte value
   ```

4. **Commit and merge** the updated `secrets.enc.yaml`. The provider is written by `inforge deploy` on
   merge — **never by the CLI** — so a deploy from `main` can never roll back a value the provider
   already serves.

5. **Restart the consumers** after the deploy lands, so they re-fetch at start:

   ```sh
   inforge service restart prd api
   ```

Adding the 2nd…Nth secret never touches your workflow — only step 1 (the one-time
`INFORGE_SECRETS_KEY`) does.

## Initialising the store

A repository uses the encrypted store once you run `inforge secret init <env>` for an environment. It
creates `resources/<env>/secrets.enc.yaml`, generates a fresh age key pair, writes the **public
recipient** into the store, and prints the **master identity** (`AGE-SECRET-KEY-…`) once:

```sh
inforge secret init prd
```

- Save the printed identity as the **`INFORGE_SECRETS_KEY`** GitHub Actions secret **in the consumer
  repo** — the deploy decrypts with it (see the [deploy starter](/github-actions/overview)).
- Keep an out-of-band backup: losing it means re-setting every secret in the environment.
- Commit the generated `secrets.enc.yaml`. Anyone with commit access can add values against the public
  recipient with no key material; reading a value requires the master identity, which is never
  committed.

Full subcommand reference (`set`, `ls`, `rm`, `rotate`) and incident-response runbooks are on the
[`inforge secret`](/cli/secret) page.

## How secrets reach a service

inforge does not bake secret values into any artifact. At deploy time, for each service that declares
secrets, inforge:

1. Resolves every `environment.yaml` entry (`ref:` from stack outputs, `vault:` by decrypting the
   store, `env:` from the deploy environment, literals verbatim) and writes the values to the secrets
   provider under the service's scoped path.
2. Mints a **per-service machine identity**, scoped read-only to that service's path.
3. Writes two files onto the host under `/etc/wardnet/services/<service>/`:
   - `descriptor.yaml` — a secret-free document with the provider coordinates and an env-var → vault-key
     mapping.
   - `credential.age` — the machine-identity credential, age-encrypted to the host's own SSH host key
     (encrypted in memory to the key read over SSH; the plaintext never lands on disk).

At service start, `inforge-bootstrap` (the systemd `ExecStart` for every service) decrypts the
credential with the host key, logs in to the provider, fetches the secrets, injects them as environment
variables, drops privilege to the service's `user`, and execs the real binary. Secret *values* live
only in the service process's environment — never on disk, in the journal, or in argv. A service that
declares no secrets gets a descriptor with no provider and starts with no fetch at all.

The secrets provider a service's values are written to is the project's **`secretsStore`** setting in
`inforge.yaml` (see [Provider defaults](/configuration/inforge-yaml#providers)). A service that
declares environment variables in a region (or global slice) with no matching secrets provider fails
`inforge validate`, before any deploy.
