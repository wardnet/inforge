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
  Pulumi program in `program/` and the CLI in `cmd/inforge/`.
- **Neon provider** (`providers/neon/`) — Pulumi plugin (`pulumi-resource-neon`) provisioning Neon
  Postgres. *Stub — no resolved domain yet; CONTEXT.md added when implemented.*
- **Infisical provider** (`providers/infisical/`) — Pulumi plugin (`pulumi-resource-infisical`)
  managing Infisical workspaces/secrets. *Stub — CONTEXT.md added when implemented.*

## Relationships

- **Toolkit → providers**: the Toolkit defines provider interfaces (`NetworkProvider`,
  `ComputeProvider`, `DnsProvider`, `DatabaseProvider`, `IngressProvider`,
  `ServiceSecretsProvisioner`) and a `ProviderRegistry`. Each provider context satisfies one or more
  of these.
- **Toolkit ↔ secrets**: services fetch their own secrets at runtime via `inforge-agent`. inforge
  writes each service's provider coordinates and a host-key-encrypted machine-identity credential to
  the host; it bakes no secret values and uses no key broker. See
  [ADR-0010](./docs/adr/0010-runtime-secret-fetch.md).
