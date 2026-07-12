# inforge-internal secrets live in the store's reserved namespace, not a service

The secret store (`internal/secretstore.Store`) has two disjoint two-level maps:

- **`Services`** (`services:` in `secrets.enc.yaml`) — service secrets, keyed by
  (service, KEY), reached via `Get`/`Set`/`Delete`/`Keys` and resolved through a service's
  `vault:<KEY>` source at deploy (`program.decryptEncryptedSecrets`).
- **`Reserved`** (`reserved:`) — inforge-internal, **env-level** secrets referenced by no service
  and read directly by the deploy, keyed by (namespace, KEY), reached via
  `GetReserved`/`SetReserved`/`DeleteReserved`/`ReservedKeys` and
  `program.decryptReservedSecret`.

An env-level credential that inforge itself consumes (today: the observability OTLP Basic-auth
credential, `otelcol.AuthSecretNamespace`/`AuthSecretKey` = `observability`/`otlp_auth`, ADR-0031;
the Grafana token, ADR-0038; the R2 backup keys, ADR-0036) is a **reserved** secret. It must **not**
be modelled as a service:

- it is written with `inforge secret set <env> <namespace> <KEY> --reserved` (the `--reserved`
  flag bypasses `requireService`, which requires a declared service);
- it is read with `decryptReservedSecret`, **independent of the `vault:` wanted-set** — routing it
  through `decryptEncryptedSecrets` would never surface it, since that returns `nil` when no
  service uses `vault:`;
- because it lives outside the service namespace, a user may name a service **anything**
  (including `observability`) without colliding — the reason this namespace exists;
- it is exempt from `validate.checkSecretStoreEntries`' strict orphan check: reserved keys are
  operator-named (e.g. a Grafana contact-point secret, ADR-0038), so there is no closed set to check
  them against. `secret set --reserved` warns against `knownReservedSecrets` instead.

Do NOT re-introduce an env-level inforge secret as a magic service name (a blocklist stealing a
plausible user name) or via a name-mangling sentinel (`inforge::…` breaks the moment a name reaches
`workspaceName`/`naming.Resource`, whose charset forbids `:`). Add it to the `reserved` namespace.

**`container:` is not a secret namespace, and must never become one again.** Until ADR-0040 the
service map was keyed by a service's *container*, so two services sharing a container silently shared
every secret while the CLI showed only service names. Secrets are keyed by the SERVICE that declares
the `vault:` reference; `container:` is a grouping/isolation label (cloud labels, URN namespace, the
hcloud network identity for a `NetworkSpec`, and eventually project segregation) with zero bearing on
secret resolution. A value two services both need is stored twice — the duplication is deliberate, so
a secret's blast radius equals the unit that rotates it.

`mesh` is different again: it is neither a service secret nor a reserved one. Mesh leaf/bundle
material never touches the secret store — it is minted at deploy/renew time and SSH-pushed directly
to each host as `leaf.age` (ADR-0035), so there is no provider-side workspace of any kind to model
here.

## Applies to

`internal/secretstore/store.go` (the `Services`/`Reserved` split + the `ns*` shared helpers),
`cmd/inforge/secret.go` (the `--reserved` flag on `set`/`ls`/`rm`),
`program/encrypted.go` (`decryptEncryptedSecrets`, `decryptReservedSecret`),
`program/secretresolve.go` (`resolveRef`), `program/program.go` (the observability read),
`internal/validate/validate.go` (`checkSecretStoreEntries`), `internal/otelcol/paths.go`
(`AuthSecretNamespace`). Any future env-level inforge secret is a new `reserved` namespace — never a
service, and never a container.

## Why

An env-level credential inforge reads for itself isn't a service's secret, and shoehorning it into
the service namespace (as the observability credential originally was, against the then-container
namespace) both stole a plausible name from users and coupled the read to a `vault:` reference that
doesn't exist — so the credential was unwritable via the CLI and unreadable at deploy. A separate
namespace makes the distinction structural: collision is impossible by construction and the read has
no false dependency.
