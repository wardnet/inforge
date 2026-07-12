---
status: accepted
date: 2026-07-12
---

# Service secrets are keyed by the service, not by its container

[ADR-0017](0017-git-native-encrypted-secret-store.md) shaped the store as `containers: { container → { KEY →
ciphertext } }`, and every `vault:<KEY>` source resolved against the declaring service's **`container:`**.
The CLI, however, has always taken a *service* name (`inforge secret set <env> <service> <KEY>`) and quietly
rewritten it to that service's container.

The two do not mean the same thing. `ddns` and `tunneller` both declare `container: edge`, so they shared one
secret namespace: `vault:OTEL_AUTH_TOKEN` in both manifests was **literally the same stored entry**, and
`inforge secret set prd ddns FOO` also wrote `tunneller`'s `FOO`. Nothing in the authoring surface said so.
The blast radius of a secret was the container; the unit an operator thought they were rotating was the
service.

For a `ServiceSpec` the container earned nothing in exchange: it is not the systemd unit name, the on-host
path, or the release artifact name (all of those derive from `svc.Name`). Its only other effect on a service
is one Cloudflare tag on the service's DNS record. Meanwhile `container:` is intended to grow into an
**infrastructure segregation boundary** (several Hetzner projects within one region) — a topology concept.
Leaving a security boundary welded to it would make that evolution a security event.

## Decisions

- **The store is keyed by service.** `services: { service → { KEY → ciphertext } }`. `vault:<KEY>` resolves
  against `svc.Name` (`program.decryptEncryptedSecrets`, `program.resolveRef`, `validate.checkService`).
  The CLI's arguments are unchanged — it was already honest; only the address it writes changed.

- **A value two services share is stored twice, on purpose.** There is no shared-secret mechanism and no
  `vault:shared/KEY` syntax. Deduplicating the ciphertext would rebuild exactly the coupling this ADR
  removes: the point is that the blast radius of a secret equals the unit that rotates it. Two services
  needing the same upstream token each hold their own entry, and either can be rotated alone.

- **`container:` keeps no secret semantics, ever.** It stays on every spec as the grouping/isolation label
  (URN namespace, cloud labels, the hcloud network identity for a `NetworkSpec`, and — in future — project
  segregation). It must not regain a secret meaning; see the rule
  `reserved-secrets-live-outside-service-namespace`.

- **`reserved:` is untouched.** Inforge-internal env-level secrets (the OTLP credential, the Grafana token,
  the R2 backup keys) stay keyed by (namespace, KEY) in their own disjoint map. They are referenced by no
  service, so a per-service key would be meaningless.

- **Migration is a hand edit, guarded by a hard load failure.** Every value is an armored age ciphertext
  encrypted to the env's recipient, so **re-keying a map entry needs no decryption**: moving an entry from
  `containers.edge` to `services.ddns` (and copying it to `services.tunneller`) is a plain YAML edit,
  reviewable in a PR, with no credentials in play. Rather than ship a migration command for a one-time
  reshuffle, `secretstore.Load` **rejects** a store still carrying a `containers:` block and says what to do.
  The guard lives in `Load` so validate, the CLI and the deploy all inherit it — and because yaml.v3 decodes
  non-strictly, a store whose `containers:` block was merely *ignored* would parse clean while serving no
  secrets at all, taking every service down at once.

- **Validation checks both directions, strictly.** Beyond "every declared `vault:` ref has a ciphertext",
  `validate.checkSecretStoreEntries` now rejects any store entry nothing reads: a key naming an undeclared
  service, or a KEY under a service that declares no `vault:` reference to it. Both are **errors**, not
  warnings — with a hand migration, the store is precisely the place a silent mismatch survives until a
  service comes up without the value it expected. The consequence is accepted: you cannot park a secret
  ahead of the service that will read it. The reserved namespace is exempt (its keys are operator-named —
  a Grafana contact-point secret, ADR-0038 — so no closed set exists to check against).

## Consequences

- Existing environments must reshuffle `secrets.enc.yaml` by hand once. `inforge validate` names every
  missing `(service, KEY)` pair and every orphaned entry, so the edit is mechanical and verifiable.
- A rotation is now genuinely per-service: `inforge secret set prd ddns OTEL_AUTH_TOKEN` no longer moves
  `tunneller`'s value. The cost is that rotating a *shared* upstream credential is N commands, one per
  consuming service — which is the honest shape of the operation.
- `inforge secret set`'s "restart the consuming service" hint is now one line, not one per container sibling.
