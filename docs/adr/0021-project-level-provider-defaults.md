---
status: accepted
date: 2026-06-12
issue: "#96"
---

# inforge.yaml gains project-level provider defaults

> _Note: the `neon`/`infisical` provider examples below are retired ([ADR-0035](0035-git-backed-per-host-secrets-delivery.md), [ADR-0036](0036-self-hosted-postgres-and-cluster-database-split.md)) — PostgreSQL is self-hosted and there is no secrets-store provider. The provider-defaults mechanism is unchanged._

Every resource spec carried a required `provider:` field declaring which provider handles it
(`provider: hetzner`, `provider: neon`). When a project uses exactly one compute provider and
one database engine across all environments, repeating `provider: hetzner` on every compute
spec and `provider: neon` on every database spec is noise with no information content. An
erroneous omission produces a validation error with no obvious fix for new users; an explicit
default removes the mandatory field from the common case.

## Decisions

- **`inforge.yaml` gains an optional `providers:` block** at the project level:
  ```yaml
  providers:
    compute: hetzner
    database:
      postgresql: neon
    secretsStore: infisical
  ```
  The sub-keys are resource classes. `database` is keyed by engine (`postgresql`) because
  different database engines may route to different providers.
- **A resource that omits `provider:` inherits from the project default for its class.**
  Resolution order: explicit spec override → project default → validation error.
- **A resource that declares `provider:` always takes precedence**, enabling mixed-provider
  environments without a special override syntax.
- **`secretsStore` is the authoritative project-level secrets provider selection.** A region's
  `providers` block may contain credentials for several providers; `secretsStore` names which
  one stores secrets for every service in this project. There is no per-resource override:
  the secrets provider is a project-wide choice, and the Infisical identity/path model relies
  on a single provider per deployment. `inforge validate` reports an error if the named
  provider's credentials are absent from any region that has secret-bearing services.
- **The `providers:` block is serialised into Pulumi stack config** and read by the Pulumi
  program at runtime. This means changing a provider default — e.g. switching the postgresql
  database provider — is a stack-config change that Pulumi will act on at the next `pulumi up`,
  potentially replacing resources. Treat a default change with the same care as an explicit
  `provider:` change on a resource.
- **An omitted `providers:` block** means all resources must declare `provider:` explicitly,
  preserving backwards compatibility.

## Considered alternatives

**Per-environment provider defaults in `regions.yaml`.** Environment-specific but still
verbose when all environments use the same provider. Rejected in favour of a single project-
level declaration.

**Infer defaults from the first resource that declares a provider.** Implicit and order-
dependent. Rejected.
