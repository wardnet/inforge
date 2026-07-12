---
status: accepted
date: 2026-06-11
issue: "#91"
---

# App secrets live age-encrypted in git; inforge provisions them into the provider

> _Note: the "write plaintext into the secrets provider; the host fetches it" delivery half was replaced by git-committed age secrets delivered per-host over SSH ([ADR-0035](0035-git-backed-per-host-secrets-delivery.md)). The git-encrypted-store half stands._
>
> _Note: the store's **namespace** is no longer the container. Values are keyed by the **service** that declares the `vault:` reference — `services:`, not `containers:` ([ADR-0040](0040-service-scoped-secrets.md)). Everything below about the store's per-value age encryption, its committed recipient, and the single deploy-side identity is unchanged; only the map key is._

Today a service's app secret reaches the provider one of two ways: an `${ENV}` source (the value is
injected into the deploy process environment — one CI-secret-to-env line per secret, in the consumer's
workflow) or a `static:` source (plaintext in git, non-secrets only). Both are unsatisfactory for real
app secrets: `${ENV}` means **adding a secret requires editing the workflow**, and it puts the secret's
*value* outside git, so the infrastructure cannot be rebuilt from the repo alone and a provider swap
(Infisical → Vault) becomes a data migration rather than a config change.

We decided to make **git the source of truth for app secret values**, stored age-encrypted, with inforge
decrypting them at deploy time and provisioning them into whatever secrets provider is configured. A new
`encrypted` source kind references a value held in a committed, age-encrypted store; an `inforge secret`
CLI manages that store. The provider becomes a *derived runtime cache*, not a source of truth.

## Relationship to ADR-0006 and ADR-0010 (read this first)

This is **not** a revival of ADR-0006 (SOPS/age manifest baking + key broker), which ADR-0010 retired.
The distinction is *where decryption happens*:

- **ADR-0006 (retired):** encrypted secret values travelled **to the host**, and the host held a path to
  decrypt them (broker redemption, then re-key to the host key). The host was in the decryption path.
- **ADR-0010 (current, unchanged):** the host fetches secret *values* from the provider at runtime with a
  per-service, read-only machine identity; inforge writes only provider *coordinates* + a host-key-encrypted
  identity credential. No secret value and no master decryption key ever lives on the host at rest.
- **ADR-0017 (this):** changes only the **deploy-time input source** for the values inforge writes into the
  provider. Encryption/decryption happens **in the deploy environment (CI)**, with a master key that lives
  exactly where the provider API credentials already live — never on the host. The runtime path of ADR-0010
  is untouched: the host still fetches from the provider, still never sees the master key or the git
  ciphertext.

So 0017 **complements** 0010 (it does not supersede it) and does **not** reintroduce 0006's host-side
decryption or any broker.

## Why the provider is still needed (the spine)

If all values live in git, why keep Infisical/Vault at all? Because **the master private key must never
reach the host.** The provider is the channel that carries *decrypted* values from the one place that holds
the master key (the deploy environment) to the host — without shipping the decryption key to every box.
inforge decrypts in CI and writes plaintext into the provider; the host fetches from the provider per
ADR-0010. That is precisely why this is not "SOPS on the host": the host has no key material that can
decrypt anything in git.

This also makes the provider genuinely swappable. Because the canonical values live in git (encrypted) and
the provider only ever holds a *projection* inforge writes, changing the provider block in `regions.yaml`
and re-running `inforge deploy` repopulates a fresh provider from git. The data was never canonically *in*
the provider.

**Value replacement rides the same single-writer path — deliberately.** An earlier draft of this ADR gave
hot value replacement a "git-first dual-write": commit the new ciphertext, then write the provider directly
so the value goes live without a deploy. That design is unsound under a protected main branch: git-first
only orders the two writes on the operator's *clone*, while deploys read *main* — so between the direct
provider write and the commit merging, any routine deploy from main **rolls the provider back to the old
value**, silently re-exposing the credential just replaced. It would also have spread the deploy-grade
provider credentials to operator machines. So replacing a value is git-only like every other store write:
`set` (an upsert) updates the ciphertext, the change merges through a reviewed PR, the deploy on merge
writes the provider, and `inforge service restart` makes the running service pick it up. The provider's standing
value in this model is concretely: (1) no secret values at rest on the host — only a revocable,
least-privilege identity credential (ADR-0010); (2) audit of fetches; and (3) the option of
dynamic/short-lived secrets later. Confidentiality from a host-root attacker is *not* on that list —
nothing on the host can provide it, and this ADR does not pretend otherwise.

## Decisions

- **New source kind `encrypted`.** In a `secrets/*.yaml` entry, `source: encrypted` declares that the
  value is held in the committed encrypted store, keyed by `(container, secret-key)`. Declaration of *which*
  secrets exist stays in `secrets/*.yaml` (one declaration point); the store holds only ciphertext.

- **The store and recipient live in the *consumer* repo, never in the inforge toolkit repo.** The ciphertext
  store and the age recipient are the consumer's infrastructure data (next to their `resources/`); inforge
  ships only the *machinery* (the `encrypted` source kind and the `inforge secret` CLI). This ADR lives in
  the toolkit repo because it records toolkit behavior, but no secret material or recipient is ever committed
  here.

- **The store is age ciphertext in git, per environment.** Values live in the consumer repo at
  `resources/<env>/secrets.enc.yaml`: a `recipient:` header (the env's committed public key) and a
  map of `container → { KEY → armored-age-ciphertext }` (superseded by ADR-0040: the map is `services:`,
  keyed by `service`). Carrying the recipient in the store
  file keeps each env's store self-contained — `set` knows what to encrypt to with no further configuration,
  and `rotate <env>` swaps the recipient and re-encrypts in one file. Per-value encryption (not a single
  encrypted blob) so a diff shows exactly which secret changed and a single value can be replaced independently.

- **A committed age *recipient* (public key); a single private *identity* at deploy.** The consumer repo
  commits the age recipient (public key) so **anyone with commit access can add a secret with no private
  key** — `inforge secret set` encrypts to the recipient. inforge decrypts at deploy with one master key,
  `INFORGE_SECRETS_KEY` (the age identity), injected into the deploy environment **once**. Adding the
  2nd…Nth secret never touches the workflow.

- **`encrypted`, `ref:`, and `${ENV}` coexist as first-class source kinds.** A `secrets/*.yaml` entry may
  source a value three ways, all explicit in the service's yaml: `ref:` (an infra output like a DB URL),
  `${ENV}` (a value injected into the deploy environment), or `encrypted` (the git-stored ciphertext). This
  ADR *adds* `encrypted`; it does not remove or deprecate the others. `encrypted` is the right default for
  app secrets that should live in git; `${ENV}` remains correct for values genuinely external to the deploy
  (and provider/cloud API credentials continue to come from `regions.yaml` `${ENV}` references). At deploy,
  every resolved secret — regardless of source kind — is written to the provider under `infra/<KEY>` and the
  bootstrapper injects it as an env var; the source kind only decides *where inforge reads the value from*.

- **Resolution is provider-neutral.** The `encrypted` resolver (decrypt-with-master-key) lives in a shared
  package, not inside the infisical provider. Providers receive *plaintext* values; they never see
  ciphertext or the master key. A future Vault provider reuses the same resolver unchanged. (This is the
  property that makes the headline provider-swap claim true.)

- **The deploy is the provider's single writer; the CLI writes only git.** No `inforge secret` subcommand
  touches the provider. `inforge secret set <env> <service> <KEY>` writes or replaces a value in the store
  (`--generate` mints a fresh random value; otherwise the value is read from stdin) and the operator commits
  and merges it like any other resources change; the deploy on merge projects it into the provider. This is
  what makes the headline invariant *enforceable*: with exactly one writer ordered by main's history, the
  provider can never be ahead of (or behind) the git state a deploy reads, and there is no window in which a
  deploy reverts a hot value (see Context for why the dual-write variant had one). A replaced value reaches
  the running process via `inforge service restart <env> <service>` after the deploy lands — services fetch
  secrets at start (ADR-0010), so a restart is the pickup. Writing git needs only the public recipient
  (never the master key and never provider credentials).

- **`rotate` rotates the master key pair, not a value.** `inforge secret rotate <env>` (alias `rekey` —
  Vault's and the age/SOPS community's word for this) mints a new identity/recipient pair (or takes
  `--recipient`), decrypts every stored value with the current `INFORGE_SECRETS_KEY` and re-encrypts to the
  new recipient. Values are unchanged, so no deploy or restart is needed — just the GitHub secret update and
  a commit. Replacing a *value* is `set`: an upsert, so a leaked credential is fixed by setting it again.
  The two compromise runbooks follow from the split: a leaked **value** → `set` that key (new key material
  does nothing for a plaintext already out); a leaked **identity** → `rotate` first (so replacements are
  encrypted to a clean recipient), **then `set` every stored value** — re-encryption alone cannot un-expose
  them, because the old ciphertexts remain decryptable in git history with the leaked identity. `rotate`
  prints exactly this guidance with a per-key command list.

- **`inforge secret` CLI takes env + service.** `set <env> <service> <KEY> [--generate]` (above), plus
  `ls <env> <service>`, `rm <env> <service> <KEY>`, `init <env>` (creates the store, minting the master key
  pair unless an existing recipient is given), and the per-env `rotate <env>` (above). The service argument
  resolves to its container (secrets are container-scoped, broadcast to every service in the container). The
  CLI is the only writer of the store. `static:` remains for non-secret inline config only.

- **`validate` checks the store.** Every `source: encrypted` must have a matching ciphertext entry for the
  env being validated; a missing entry fails `validate`, turning a late runtime miss into an early error.

## Consequences

- **Rebuild-from-scratch is git + the age private key + provider API creds** — not "git alone." Cloning the
  repo and running `inforge deploy` with `INFORGE_SECRETS_KEY` and the provider/cloud credentials
  reconstructs every app secret into a fresh provider. `ref:`-sourced values (e.g. DB connection URLs) are
  reconstructed by provisioning, not stored. The precise claim is: **no secret value is unrecoverable from
  the repo given the one master key.**

- **The master key is the single root of trust.** Its loss means the encrypted store cannot be decrypted
  (back it up out-of-band); its compromise exposes every app secret. Repo access *alone* discloses nothing —
  the ciphertext needs the private key. This is the same trust profile as the provider credentials the
  deploy already holds.

- **A committed public recipient means anyone who can commit can *overwrite* a secret value** (encrypting to
  the recipient needs no private key). They cannot *read* existing values. PR review is the control; record
  it as an accepted property, not a leak.

- **Two distinct age usages now coexist.** ADR-0010's host-credential encryption (`internal/agehost`,
  shaped around the host's SSH key) and this store's authoring encryption (a standalone X25519 age
  identity). They are separate key materials and separate code paths; the store does not reuse `agehost`.

- **Partially walks back ADR-0006's "secrets in git" stance — deliberately, and at a different layer.**
  0006 was retired for putting decryption *on the host*; 0017 keeps decryption in the deploy environment, so
  the reasons 0010 cited against 0006 (key travels to the host, third-party broker, OIDC coupling, host
  re-keying) do not apply.

## Considered alternatives

- **Operator-managed secrets in the provider under `custom/`** (the dormant namespace the bootstrapper
  already supports). Rejected as the primary model: it makes the *provider* a source of truth, so the infra
  cannot be rebuilt from the repo and a provider swap becomes a data migration — the exact properties this
  ADR exists to get. (`custom/` may still be wired later for genuinely operator-only secrets, but it is not
  the answer to "add a secret in git, no workflow change.")

- **A single SOPS-style whole-file encrypted blob.** Rejected in favour of per-value ciphertext: a blob
  produces opaque diffs (every change rewrites the file) and blocks per-key rotation.

- **Keep `${ENV}` and just script the workflow injection.** Rejected: it leaves the value outside git
  (no rebuild, no clean provider swap) and still grows the workflow per secret — it does not meet either
  stated goal.

- **Drop the provider entirely; seal values to the host key.** A real contender: inforge already encrypts
  the per-service identity credential to the host's SSH key (ADR-0010), so the same machinery could encrypt
  the secret *values* to the host key and have the bootstrapper decrypt them at runtime — no Infisical, fully
  git-driven, one fewer dependency. (This is *not* the retired ADR-0006: no broker, no OIDC exchange, no third
  party.) Rejected because it puts secret values at rest on every host and gives up three properties we want:
  a leaked host key would mean re-keying every secret on that host and redeploying everywhere (vs. revoking
  one scoped identity), there is no fetch audit, and there is no path to dynamic/short-lived secrets.
  The sealed-to-host model remains a reasonable choice for a deployment that values simplicity over those
  properties; this ADR does not foreclose it as a future provider-less mode.
