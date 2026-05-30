# Context Map

This repository builds three statically-linked binaries that form three bounded contexts: the
`inforge` toolkit/CLI and two Pulumi provider plugins. Only the **Toolkit** context has a resolved
domain language today; the provider contexts are stubs and gain their own `CONTEXT.md` when
implemented.

## Contexts

- [Toolkit](./internal/CONTEXT.md) — the declarative-infrastructure domain: resources, regions,
  sizes, services, manifests, and the VM bootstrap/deployment model. Lives in `internal/`, with the
  Pulumi program in `program/` and the CLI in `cmd/inforge/`.
- **Neon provider** (`providers/neon/`) — Pulumi plugin (`pulumi-resource-neon`) provisioning Neon
  Postgres. *Stub — no resolved domain yet; CONTEXT.md added when implemented.*
- **Infisical provider** (`providers/infisical/`) — Pulumi plugin (`pulumi-resource-infisical`)
  managing Infisical workspaces/secrets. *Stub — CONTEXT.md added when implemented.*

## Relationships

- **Toolkit → providers**: the Toolkit defines provider interfaces (`NetworkProvider`,
  `ComputeProvider`, `DnsProvider`, `DatabaseProvider`, `SecretsBackendProvider`) and a
  `ProviderRegistry`. Each provider context satisfies one or more of these. At this phase the
  registry is a stub that returns `unknown provider` for every lookup.
- **Toolkit ↔ external escrow**: the Toolkit *integrates* with an external, multi-tenant bootstrap
  escrow service (owned in `wardnet-infrastructure`) for first-boot secret decryption; it does not
  implement it. See [ADR-0006](./docs/adr/0006-bootstrap-escrow-and-sops-age.md).
