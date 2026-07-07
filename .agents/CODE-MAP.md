# Code Map — inforge

> Inforge is a Go toolchain that turns declarative infrastructure definitions into real deployments via Pulumi and GitHub Actions. Hash-verified — manual edits preserved unless underlying files change.

**Last updated:** 2026-05-31

## Conventions

- **Module path:** `github.com/wardnet/inforge`
- **Go version:** 1.25.8 (CI/release on 1.26)
- **Build tool:** goreleaser v2 (three binaries: `inforge` CLI + two Pulumi provider plugins)
- **Lint:** golangci-lint v2 with `errcheck`, `govet`, `staticcheck`, `unused`; v2 schema means no separate `gosimple`
- **Provider binary names are load-bearing:** Pulumi locates plugins by filename `pulumi-resource-<name>` exactly
- **Binaries must be statically linked:** all builds set `CGO_ENABLED=0`; no cgo dependencies
- **Version injection:** `cmd/inforge/main.go` exposes `var version = "dev"`, overridden at release via `-ldflags "-X main.version=<tag>"`
- **Config patterns:** projects use `inforge.yaml` (default) and `resources/<env>/` directory structure; `.github/dependabot.yml` manages gomod + github-actions

## Areas

### CLI Entry Point

**Where:** `cmd/inforge/`

**Summary:** Single `main.go` file using Cobra for command dispatch. Currently implements only the `validate <env>` subcommand (calls `internal/validate.ValidateResources`). Missing: `preview`, `deploy`, `matrix`, `plugins`, `version` subcommands per issue #8. The root command wires persistent flags `--dir` (default `./resources`) and `--config` (default `./inforge.yaml`).

**Entry points:** `cmd/inforge/main.go`

**Tracked files:**
- `cmd/inforge/main.go` (blob: `edb094054a0f2b4f182bf29400d70a169390565e`)

### Config Loading & Project Structure

**Where:** `internal/loader/`, `internal/types/`, `.github/workflows/deploy-raw.yml`

**Summary:** Projects are structured as `inforge.yaml` (config) + `resources/<env>/variables.yaml` (env-level variables + region/provider declarations) + per-region subdirectories containing YAML specs for network, compute, dns, database, secrets, service. The loader reads and parses these files, applies defaults, substitutes `${ENV_VAR}` references, and returns typed structs. No explicit config file reading in program.go yet—it reads `dir` from Pulumi config context, not `inforge.yaml`.

**Entry points:** `internal/loader/loader.go` (exported: `LoadVariables`, `LoadRegionTable`, `LoadResources`, `NormalizeNetwork`, `NormalizeCompute`, `NormalizeDatabase`, `NormalizeService`), `internal/types/types.go` (resource specs: `NetworkSpec`, `ComputeSpec`, `DnsSpec`, `DatabaseSpec`, `SecretsSpec`, `ServiceSpec`)

**Tracked files:**
- `internal/loader/loader.go` (blob: `801ef6fed59052b502fb2d68e442d08c0168ef6d`)
- `internal/types/types.go` (blob: `740a28b232c9128d6339be396eeb01205cc3449e`)
- `go.mod` (blob: `3852d940ca4a94c9b8503673ce05f140bf450f73`)

### Validation

**Where:** `internal/validate/`

**Summary:** Validates resource files per environment. Reads all `.yaml` files in `resources/<env>/<resource-type>/`, validates against embedded JSON Schemas, then runs semantic checks (provider availability, CIDR hierarchy, foreign keys, secrets source DSL). Exported entry point: `ValidateResources(env, dir)`. Uses `internal/sizes.Table` and `internal/regions.Table` for constraint checking. Schemas compiled at runtime from `schemas/` (via `schemas.FS` embed).

**Entry points:** `internal/validate/validate.go` (exported: `ValidateResources`)

**Tracked files:**
- `internal/validate/validate.go` (blob: `8e359676f36f82f4ff51c49dcb8a325fa8c9d187`)

### Pulumi Program (program.go)

**Where:** `program/program.go`

**Summary:** The Pulumi program that consumes resolved resources and provisions infrastructure. Reads `environment` and optional `dir` from Pulumi config context, loads variables/resources/region-table, builds a deploy descriptor (for the reusable deploy workflow), then loops per region iterating through network/compute/database/secrets/dns specs. Calls provider methods from a registry (built per region, merging global + region-specific providers). Outputs deploy descriptor to stack. Currently a compiling stub—provider lookups return "unknown provider" until compute-provider PR wires real implementations.

**Entry points:** `program/program.go` (exported: `main()`, internal: `run(ctx)`, `assembleManifest()`, `subdomainFor()`)

**Tracked files:**
- `program/program.go` (blob: `e74de111510630589ce114b4aa676c58675b469e`)

### Service Deployment Scaffolding

**Where:** `internal/service/`

**Summary:** Models host-side scaffolding: systemd unit files, per-service `/srv/wardnet/<name>` folders. Exports `DeployTarget` and `DeployDescriptor` (YAML-serializable)—derived purely from resolved resources, used by the reusable `deploy-raw` workflow. Key exports: `BuildDeployDescriptor(env, baseDomain, byRegion, table)`, `Folder()`, `UnitName()`, `Unit()`.

**Entry points:** `internal/service/service.go` (exported: `BuildDeployDescriptor`, `Folder`, `UnitName`, `Unit`)

**Tracked files:**
- `internal/service/service.go` (blob: `0038f20cb7c52cbf201eef4d2204cdb34a32e6d8`)

### GitHub Workflows & CI

**Where:** `.github/workflows/` and `.github/actions/`

**Summary:** Three workflows exist: `ci.yml` (lints + builds + tests on PRs to main), `release.yml` (goreleaser on `v*` tags), `deploy-raw.yml` (reusable workflow called by service repos). Missing: `preview.yml`, `deploy.yml`, `reconcile.yml` (per issue #8). No `.github/actions/install/` directory exists yet. CI uses Go 1.26, golangci-lint v2.12.2, runs `go build ./...`, `go test -race ./...`.

**Entry points:** `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.github/workflows/deploy-raw.yml`

**Tracked files:**
- `.github/workflows/ci.yml` (blob: `e928de63e0fcd62542b4adc5647fcb0c115b5399`)
- `.github/workflows/release.yml` (blob: `f4851f29096baf9c10118c5c555ba9232d1afbf8`)
- `.github/workflows/deploy-raw.yml` (blob: `d375d01b292e3a1e49858e3298a9e2496a882f3a`)

### Build & Release Configuration

**Where:** `.goreleaser.yml`, `.golangci.yml`

**Summary:** goreleaser v2 builds two binaries (inforge CLI, inforge-agent) across linux/darwin/windows × amd64/arm64. The bundled Pulumi provider plugins were retired (Infisical by ADR-0035, Neon by ADR-0036), so no `pulumi-resource-*` binary is built. Produces per-binary archives (so consumers download only what they need) plus raw uncompressed binaries. golangci-lint v2 enables `errcheck`, `govet`, `staticcheck` (which includes old `gosimple`), and `unused`; all linters must pass.

**Entry points:** `.goreleaser.yml`, `.golangci.yml`

**Tracked files:**
- `.goreleaser.yml` (blob: `31b41aeb712c0c088d6bf0b9df30d82f956d5e3b`)
- `.golangci.yml` (blob: `932a42a9787b96802ffb7bf7d1900907a13af96d`)

### Provider Structure

**Where:** `providers/` (hetzner, cloudflare)

**Summary:** No provider has a custom Pulumi resource plugin anymore — the Neon (ADR-0036) and Infisical (ADR-0035) plugins were retired. Hetzner and Cloudflare are provider SDK packages (not Pulumi plugins). `inforge plugins install` downloads the standard published Pulumi plugins a project needs (e.g. `pulumi-random`) from their upstream releases.

**Entry points:** `providers/hetzner/`, `providers/cloudflare/` (Go SDK packages)

**Tracked files:** (see per-provider directories for detailed hashes)

