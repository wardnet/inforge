# inforge-internal secrets live in the store's reserved namespace, not a service container

The secret store (`internal/secretstore.Store`) has two disjoint two-level maps:

- **`Containers`** (`containers:` in `secrets.enc.yaml`) — service secrets, keyed by
  (container, KEY), reached via `Get`/`Set`/`Delete`/`Keys` and resolved through a service's
  `vault:<KEY>` source at deploy (`program.decryptEncryptedSecrets`).
- **`Reserved`** (`reserved:`) — inforge-internal, **env-level** secrets referenced by no service
  and read directly by the deploy, keyed by (namespace, KEY), reached via
  `GetReserved`/`SetReserved`/`DeleteReserved`/`ReservedKeys` and
  `program.decryptReservedSecret`.

An env-level credential that inforge itself consumes (today: the observability OTLP Basic-auth
credential, `otelcol.AuthSecretNamespace`/`AuthSecretKey` = `observability`/`otlp_auth`, ADR-0031)
is a **reserved** secret. It must **not** be modelled as a service container:

- it is written with `inforge secret set <env> <namespace> <KEY> --reserved` (the `--reserved`
  flag bypasses `resolveServiceContainer`, which requires a declared service);
- it is read with `decryptReservedSecret`, **independent of the `vault:` wanted-set** — routing it
  through `decryptEncryptedSecrets` would never surface it, since that returns `nil` when no
  service uses `vault:`;
- because it lives outside the container namespace, a user service may use **any** container name
  (including `observability`) without colliding — the reason this namespace exists.

Do NOT re-introduce an env-level inforge secret as a magic container name (a blocklist stealing a
plausible user name) or via a name-mangling sentinel (`inforge::…` breaks the moment a name reaches
`workspaceName`/`naming.Resource`, whose charset forbids `:`). Add it to the `reserved` namespace.

`mesh` is different: it is not a container or a reserved secret at all. Mesh leaf/bundle material
never touches the secret store — it is minted at deploy/renew time and SSH-pushed directly to each
host as `leaf.age` (ADR-0035), so there is no provider-side workspace of any kind to model here.

## Applies to

`internal/secretstore/store.go` (the `Containers`/`Reserved` split + the `ns*` shared helpers),
`cmd/inforge/secret.go` (the `--reserved` flag on `set`/`ls`/`rm`),
`program/encrypted.go` (`decryptReservedSecret`), `program/program.go` (the observability read),
`internal/otelcol/paths.go` (`AuthSecretNamespace`). Any future env-level inforge secret is a new
`reserved` namespace, never a container.

## Why

An env-level credential inforge reads for itself isn't a service's secret, and shoehorning it into
the container namespace (which is how the observability credential was originally modelled) both
stole a plausible container name from users and coupled the read to a service `vault:` reference
that doesn't exist — so the credential was unwritable via the CLI and unreadable at deploy. A
separate namespace makes the distinction structural: collision is impossible by construction and the
read has no false dependency.
