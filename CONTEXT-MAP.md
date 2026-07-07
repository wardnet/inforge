# Context Map

This repository builds four statically-linked binaries across three bounded contexts: the
`inforge` toolkit/CLI (which also ships the `inforge-agent` on-host runtime agent) and two
Pulumi provider plugins. Only the **Toolkit** context has a resolved
domain language today; the provider contexts are stubs and gain their own `CONTEXT.md` when
implemented.

## Contexts

- [Toolkit](./internal/CONTEXT.md) — the declarative-infrastructure domain: resources, regions,
  sizes, services, manifests, and the VM bootstrap/deployment model. Resources are folder-based
  (`regional/<type>/<name>/manifest.yaml`; sidecars alongside). Lives in `internal/`, with the
  Pulumi program in `program/` and the CLI in `cmd/inforge/`. PostgreSQL is self-hosted on
  inforge-provisioned compute (ADR-0036); the managed Neon provider was retired.

## Relationships

- **Toolkit → providers**: the Toolkit defines provider interfaces (`NetworkProvider`,
  `ComputeProvider`, `DnsProvider`, `DatabaseProvider`, `IngressProvider`). Each provider context
  satisfies one or more of these. Secrets delivery is **not** a provider — it is intrinsic to the
  Toolkit (see below).
- **Toolkit ↔ secrets**: services decrypt their own secrets at start via `inforge-agent`. inforge
  resolves each service's secret values from the git-committed encrypted store and writes them,
  age-encrypted directly to the host's own SSH key (`secrets.age`), to the host; it bakes no
  plaintext into any artifact and uses no runtime key broker or backend. See
  [ADR-0035](./docs/adr/0035-git-backed-per-host-secrets-delivery.md) (superseding the Infisical
  fetch described in the now-retired parts of
  [ADR-0010](./docs/adr/0010-runtime-secret-fetch.md)).
