---
sidebar_position: 6
---

# inforge secret

Manage an environment's **git-committed encrypted secret store** — the file behind the
[`vault:` source kind](/resources/secrets#vaultkey). Values live age-encrypted at
`resources/<env>/secrets.enc.yaml`, keyed by `(service, KEY)`; this CLI is the only writer of that
file. See [ADR-0017](https://github.com/wardnet/inforge/blob/main/docs/adr/0017-git-native-encrypted-secret-store.md)
and [ADR-0040](https://github.com/wardnet/inforge/blob/main/docs/adr/0040-service-scoped-secrets.md).

:::info Git-only by design
No `inforge secret` subcommand ever writes to a host. The value only reaches a service via
`inforge deploy`, which resolves the committed store, age-encrypts the result directly to each
host's own SSH key, and pushes it as `secrets.age` — a single writer, so a deploy can never roll back
a value already delivered. The flow for every change is: **write the store → commit & merge →
`inforge deploy` pushes the resolved value and reload-or-restarts the affected service automatically**
(the push is hash-gated, so it only runs — and only reloads/restarts — when a value actually changed;
no separate restart step is needed).
:::

## Subcommands

| Command | Purpose |
|---------|---------|
| `inforge secret init <env>` | Create the env's store; generates the master key pair (or takes `--recipient`). |
| `inforge secret set <env> <service> <KEY>` | Write or replace a **value** — stdin, or `--generate` for a fresh random one. |
| `inforge secret ls <env> <service>` | List the keys stored for the service. |
| `inforge secret rm <env> <service> <KEY>` | Remove a value from the store. |
| `inforge secret rotate <env>` | Rotate the **master key pair** and re-encrypt the store (alias: `rekey`). |

Note the split: `set` changes a *value* (it is an upsert — fixing a leaked credential is just setting
it again), `rotate` changes the *key pair* the store is encrypted to.

Secrets are **service-scoped**: the `<service>` argument is the store key itself. Two services that
share a `container` share no secrets — if both need the same upstream token, each holds its own entry
and each is rotated on its own. That is deliberate: a secret's blast radius is the service you rotate.

### Reserved secrets (`--reserved`)

A few secrets are consumed by **inforge itself** at deploy, not by a service:

- the observability OTLP Basic-auth credential (`observability/otlp_auth`, needed when
  `observability.otlp_endpoint` is set);
- the Postgres **backups** R2 credential — two keys `backups/r2_access_key_id` and
  `backups/r2_secret_access_key` (needed when `backups.bucket` is set and any database has backups
  enabled). See [Database — Backups](../resources/database#backups).

These live in a separate **reserved namespace** in the store, outside the per-service namespace,
so they don't need a backing service and a service may still carry the same name.
Pass `--reserved` on `set`/`ls`/`rm` and the second argument is the reserved namespace instead of a
service:

```bash
# Grafana Cloud hands you an "instanceID:token" for OTLP Basic auth:
pbpaste | inforge secret set prd observability otlp_auth --reserved
inforge secret ls prd observability --reserved

# A backup-scoped R2 API token (object read/write on the backups bucket only):
printf %s "$R2_ACCESS_KEY_ID"     | inforge secret set prd backups r2_access_key_id --reserved
printf %s "$R2_SECRET_ACCESS_KEY" | inforge secret set prd backups r2_secret_access_key --reserved
```

The value is read directly by `inforge deploy` (no service restart), so commit the store and let the
deploy pick it up.

## The store file

```yaml title="resources/prd/secrets.enc.yaml"
# managed by `inforge secret` — do not edit by hand
recipient: age1…                # the committed public key all values are encrypted to
services:
  api:
    STRIPE_API_KEY: |
      -----BEGIN AGE ENCRYPTED FILE-----
      …
      -----END AGE ENCRYPTED FILE-----
```

Per-value armored ciphertext means a diff shows exactly which secret changed, and one value can be
replaced without touching its neighbours. The committed `recipient` is a *public* key: anyone with
commit access can add or overwrite a value (PR review is the control) **with no key material on
their machine**, but *reading* any value requires the master identity, which is never committed.

## `inforge secret init`

```
inforge secret init <env> [--recipient age1…]
```

Creates `resources/<env>/secrets.enc.yaml`. Without `--recipient` it generates a fresh X25519 key
pair, writes the public half into the store, and prints the private **master identity**
(`AGE-SECRET-KEY-…`) — to stdout, once, never stored by inforge:

- save it as the `INFORGE_SECRETS_KEY` GitHub Actions secret **in the consumer repo** (the deploy
  decrypts with it — see the [deploy starter](/github-actions/overview));
- keep an out-of-band backup: losing it means re-setting every secret in the env.

## `inforge secret set`

```
pbpaste | inforge secret set prd bridge STRIPE_API_KEY
inforge secret set prd bridge SESSION_KEY --generate
```

Encrypts one value to the store's recipient and saves the store — needing **no private key**: the
committed public recipient is all it takes, and the writer cannot decrypt what they (or anyone else)
wrote. The value comes from **stdin** (pipe it; one trailing newline is stripped), or `--generate`
mints 32 random bytes (base64url, 43 chars) in-process — the plaintext is never displayed, which is
ideal for secrets nothing external needs to know (session/signing keys, HMAC secrets, internal
tokens).

Declare the key with `vault:<KEY>` on a consuming service in its `environment.yaml` (e.g.
`resources/<env>/regional/service/<name>/environment.yaml`, with `API_TOKEN: vault:API_TOKEN`) — [`inforge validate`](/cli/validate) cross-checks declarations against
the store in both directions (a declared key with no ciphertext fails validation; `ls` flags stored
keys that no service references).

After merging, `inforge deploy` pushes the resolved value to each consumer's host and
reload-or-restarts it automatically — no separate restart step needed.

## `inforge secret rotate`

```
inforge secret rotate <env> [--recipient age1…]      # alias: rekey
```

Rotates the env's **master key pair**: decrypts every value in the store with the *current* identity
(read from `INFORGE_SECRETS_KEY`) and re-encrypts to a new recipient — freshly generated (the new
identity is printed once; update the `INFORGE_SECRETS_KEY` GitHub secret before the next deploy) or
given via `--recipient`. Plaintext values are unchanged, so no deploy or service restart is needed;
commit the rewritten store.

### It re-keys the PKI store too

The master recipient also owns the **CI-held key material in `resources/<env>/pki.enc.yaml`** — every
two-tier PKI's per-scope **intermediate key**, and a root-only PKI's **root key** (the online issuer
key). Rotating the secret store alone would leave that material readable only by the identity you are
rotating away from, breaking the next `inforge deploy` and every `inforge pki renew`. So `rotate`
re-encrypts both files and rewrites `pki.enc.yaml`'s `recipient:` header — **commit both stores**.

A two-tier PKI's **cold root** is encrypted to the store's separate `rootRecipient` (the offline
operator identity), which a master-key rotation does not touch: `INFORGE_PKI_ROOT_KEY` is *not*
needed to run `rotate`, and no key is left undecryptable. If the PKI store's `recipient:` is not the
secret store's current one (two stores initialized to different recipients), `rotate` refuses and
tells you — it will not write material the new identity cannot read.

`rotate` writes `pki.enc.yaml` first, then `secrets.enc.yaml`; if it dies between the two, re-run it
with the old identity and `--recipient <new>` — the PKI re-key is a no-op once its recipient already
matches.

## Incident response

**A secret value leaked** (shows up in a log, a paste, a breach). Key rotation does nothing here —
the plaintext is out and still valid. Reissue the credential at its vendor (or `--generate` an
internal one) and `set` it; merge, deploy, restart:

```
pbpaste | inforge secret set prd bridge STRIPE_API_KEY
```

Commit and merge — `inforge deploy` pushes the new value and restarts the service automatically.

**The master identity leaked** (`INFORGE_SECRETS_KEY` exposed). Two steps, in this order:

1. `inforge secret rotate <env>` — new key pair, so everything you write next is encrypted to a
   clean recipient. This also re-keys the PKI store's CI-held keys (see above); commit both stores
   and update the GitHub secret.
2. **`set` every value in the store.** Re-encryption alone does **not** protect the existing values:
   the old ciphertexts remain in git history, decryptable with the leaked identity, and you must
   assume they were already read. `rotate` prints the full per-key command list to make this step
   mechanical; externally-issued credentials must also be reissued at their vendor.
3. **Treat every CI-held PKI key as compromised.** The leaked identity decrypted the old
   `pki.enc.yaml` in git history too, so the mesh intermediate keys (and any root-only issuer key)
   are out. Re-keying them to a clean recipient does not un-expose them: re-mint each scope's
   intermediate with a fresh key —
   [`inforge pki recover-intermediate <env> <name> <scope>`](/runbooks/pki-recover-intermediate) —
   then `inforge pki renew <env>` to replace every leaf it signed.
