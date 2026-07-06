# Ephemeral env identity and config source must never be conflated

`program.Run` splits every downstream call into two categories (ADR-0028):

- **Config-source calls** use `srcEnv` (= `source_environment` stack config): `loader.LoadResources`,
  `loader.LoadGlobalResources`, `loader.LoadVariables`, `secretstore.Load`, `pki.Load`, and any
  path that reads `resources/<env>/`. `srcEnv` selects *what* to deploy.
- **Identity calls** use `env` (= `environment` stack config / slug): every cloud resource name
  (`naming.Resource`), FQDN derivation, Neon/Hetzner tag, SPIFFE URI scope, and Pulumi
  stack output. `env` (the slug) determines *who* the deployment is.

For a static env `srcEnv == env`, so the split is invisible. For an ephemeral env they differ
intentionally: the clone reads the source's resource tree while naming everything under the slug.

## Applies to

`program/program.go` (the `Run` entry point), `cmd/inforge/ephemeral_up.go`
(`mintReplicatedServiceLeaf` / `renewMeshCertsAs` calls), and any future call site that reads infra
state or names a cloud object.

## Why

Switching a loader call from `env` to `srcEnv` (or vice versa) silently corrupts an ephemeral run:
using the slug for a loader path reads an empty/absent tree; using srcEnv for a resource name bakes
the source's identity into the clone's cloud objects, preventing both environments from coexisting.
The two-variable invariant is the only thing that makes a network-segregated clone possible without
duplicating any resource definition.
