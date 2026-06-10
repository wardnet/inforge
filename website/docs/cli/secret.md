---
sidebar_position: 6
---

# inforge secret

Manage an environment's **git-committed encrypted secret store** — the file behind the
[`encrypted` source kind](/resources/secrets#encrypted). Values live age-encrypted at
`resources/<env>/secrets.enc.yaml`, keyed by `(container, KEY)`; this CLI is the only writer of that
file. See [ADR-0017](https://github.com/wardnet/inforge/blob/main/docs/adr/0017-git-native-encrypted-secret-store.md).

:::info Git-only by design
No `inforge secret` subcommand ever writes the secrets provider. The provider is updated exclusively
by `inforge deploy` when the committed store change merges — a single writer, so a deploy can never
roll back a value the provider already serves. The flow for every change is: **write the store →
commit & merge → deploy projects it into the provider → `inforge service restart` makes the running
service pick it up** (services fetch secrets at start).
:::

## Subcommands

| Command | Purpose |
|---------|---------|
| `inforge secret init <env>` | Create the env's store; generates the master key pair (or takes `--recipient`). |
| `inforge secret set <env> <service> <KEY>` | Encrypt a value (from stdin) into the store. |
| `inforge secret rotate <env> <service> <KEY>` | Replace a value — stdin, or `--generate` for a fresh random one. |
| `inforge secret ls <env> <service>` | List the keys stored for the service's container. |
| `inforge secret rm <env> <service> <KEY>` | Remove a value from the store. |
| `inforge secret rekey <env>` | Re-encrypt every stored value to a new recipient. |

The `<service>` argument resolves to its **container**: secrets are container-scoped, so every
service sharing the container consumes the same values, and the commands tell you which services are
affected.

## The store file

```yaml title="resources/prd/secrets.enc.yaml"
# managed by `inforge secret` — do not edit by hand
recipient: age1…                # the committed public key all values are encrypted to
containers:
  bridge:
    STRIPE_API_KEY: |
      -----BEGIN AGE ENCRYPTED FILE-----
      …
      -----END AGE ENCRYPTED FILE-----
```

Per-value armored ciphertext means a diff shows exactly which secret changed, and one key rotates
without touching its neighbours. The committed `recipient` is a *public* key: anyone with commit
access can add or overwrite a value (PR review is the control), but **reading** any value requires
the master identity, which is never committed.

## `inforge secret init`

```
inforge secret init <env> [--recipient age1…]
```

Creates `resources/<env>/secrets.enc.yaml`. Without `--recipient` it generates a fresh X25519 key
pair, writes the public half into the store, and prints the private **master identity**
(`AGE-SECRET-KEY-…`) — to stdout, once, never stored by inforge:

- save it as the `INFORGE_SECRETS_KEY` GitHub Actions secret (the deploy decrypts with it — see the
  [deploy starter](/github-actions/overview));
- keep an out-of-band backup: losing it means re-setting every secret in the env.

## `inforge secret set` / `inforge secret rotate`

```
pbpaste | inforge secret set prd bridge STRIPE_API_KEY
inforge secret rotate prd bridge SESSION_KEY --generate
```

Both encrypt one value to the store's recipient and save the store — `rotate` is the
intent-revealing form for replacing an existing value. The value comes from **stdin** (pipe it; one
trailing newline is stripped), or for `rotate --generate` the CLI mints 32 random bytes
(base64url, 43 chars) in-process — the plaintext is never displayed, which is ideal for secrets
nothing external needs to know (session/signing keys, HMAC secrets, internal tokens).

Declare the key with `source: encrypted` in the container's `secrets/*.yaml` spec —
[`inforge validate`](/cli/validate) cross-checks declarations against the store in both directions
(a declared key with no ciphertext fails validation; `ls` flags stored keys that are not declared).

After merging, the deploy writes the provider; then restart the consumers:

```
inforge service restart prd api
```

## `inforge secret rekey`

```
inforge secret rekey <env> [--recipient age1…]
```

Decrypts every value in the env's store with the **current** master identity (read from
`INFORGE_SECRETS_KEY`) and re-encrypts to a new recipient — freshly generated (the new identity is
printed once; update the `INFORGE_SECRETS_KEY` GitHub secret before the next deploy) or given via
`--recipient`. Plaintext values are unchanged, so no service restart is needed; commit the rewritten
store.
