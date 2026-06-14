# inforge — agent guide

Inforge is a Go toolchain that turns declarative infrastructure definitions into real deployments
via Pulumi and GitHub Actions. This repository builds four statically-linked binaries: the `inforge`
CLI, the `inforge-bootstrap` runtime secret bootstrapper (every service's systemd ExecStart), and two
Pulumi provider plugins (`pulumi-resource-neon`, `pulumi-resource-infisical`).

## Commands

```sh
go build ./...                 # build all binaries
go test -race ./...            # run tests (race detector on, as CI does)
golangci-lint run ./...        # lint — must be clean before a PR
go run ./cmd/inforge           # run the CLI locally

# Release build dry-run (produces dist/ — four binaries × os/arch):
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
```

## Layout

```
cmd/inforge/                                       # the inforge CLI (user-facing)
cmd/inforge-bootstrap/                             # runtime secret bootstrapper (service ExecStart)
internal/bootstrapper/                             # bootstrapper core (descriptor, fetch, decrypt, exec)
internal/pki/                                      # PKI store (pki.enc.yaml read/write), x509 helpers, ScopeGlobal const
internal/validate/                                 # inforge validate — structural checks incl. credential-free PKI pass
providers/neon/cmd/pulumi-resource-neon/           # Pulumi provider plugin — Neon
providers/infisical/cmd/pulumi-resource-infisical/ # Pulumi provider plugin — Infisical
.goreleaser.yml                                    # build/release config (v2 schema)
.golangci.yml                                      # lint config (v2 schema)
.github/workflows/{ci,release}.yml                 # CI on PRs to main; release on v* tags
.github/dependabot.yml                             # gomod + github-actions, 5-day cooldown
```

- Module path: `github.com/wardnet/inforge`. Go directive: `go 1.25.8` (floored by the Pulumi SDK;
  CI/release build on Go 1.26).
- The two `pulumi-resource-*` binaries are Pulumi plugins, **not** user commands. They are installed
  automatically by `inforge plugins install`, which downloads only the providers a project needs from
  the matching GitHub release. Users never invoke them directly.

## Resource naming convention

All cloud resource names follow `wardnet-<env>-<regionSlug>-<type>-<name>[-<NN>]`.

| Token | Example | Source |
|---|---|---|
| `wardnet` | fixed | `naming.usage` const |
| `env` | `prd` | environment name |
| `regionSlug` | `use1` | `regions.Table.Slug(region)` |
| `type` | `vm`, `fw`, `net`, `subnet`, `db`, `project`, `secrets`, `workspace`, `record`, `ingress`, `svc`, `identity` | resource type token |
| `name` | `bridge` | spec name |
| `NN` | `01` | instance index (servers only) |

SSH keys are env-scoped (no region): `wardnet-<env>-key-user` / `wardnet-<env>-key-deploy`.

Three functions in `internal/naming` build these names:

```go
naming.Resource(env, slug, "vm", "bridge")              // wardnet-prd-use1-vm-bridge
naming.ResourceInstance(env, slug, "vm", "bridge", 1)   // wardnet-prd-use1-vm-bridge-01
naming.GlobalResource(env, "key", "user")               // wardnet-prd-key-user
```

`naming.SpecKey(name, instance)` produces `bridge-01` etc. and is used as an internal map key and
in derived names (DNS records, display names). It is NOT a cloud resource name and is NOT written in
resource specs — foreign references use the resource `name` directly (e.g. `service.host: bridge`).

## Mesh PKI

Every service manifest requires a `pki:` field naming the **two-tier** (mesh) PKI in `pki.enc.yaml`
the service joins as a leaf member. The name is a FK into the `pki.enc.yaml` store — it must resolve
to a PKI with topology `two-tier` and an intermediate for every scope the service deploys under:

- **Global services** (`global/service/…`) → must have an intermediate for scope `"global"`.
- **Regional services** (`regional/service/…`) → must have an intermediate for every region defined
  in `regions.yaml` (the regional set deploys to all regions simultaneously).

`inforge validate` enforces these rules credential-free (reads only the store's plaintext structure —
no decryption keys needed). A missing intermediate fails validation with a command hint:
`inforge pki intermediate <env> <pki-name> <scope>`.

An environment may host several meshes; a service names the one it joins. The mesh trust model (per-scope
bundles + acceptor-side authz) and leaf delivery are implemented in slices #108/#109 — not here.

## Conventions

- **Provider binary names are load-bearing.** Pulumi locates plugins by the exact filename
  `pulumi-resource-<name>`. Never rename these binaries or their `cmd/` directories.
- **Binaries must stay fully self-contained.** All goreleaser builds set `CGO_ENABLED=0` so the output
  is statically linked — no Go runtime or shared libraries needed to run them, and provider downloads
  in CI require no Go toolchain. Do not introduce cgo dependencies.
- **Version injection:** `cmd/inforge` exposes `var version = "dev"`, overridden at release via
  `-ldflags "-X main.version=<tag>"`. Keep that variable name and package stable.
- **goreleaser & golangci-lint both use the v2 config schema.** In golangci-lint v2, `gosimple` is part
  of `staticcheck` — don't add it as a separate linter (it will error).
- Lint must pass with zero issues; `errcheck` is on, so check returned errors (e.g. prefer
  `fmt.Println` over an unchecked `fmt.Fprintf`).

## Boundaries

- **Always:** run `go build ./...`, `go test -race ./...`, and `golangci-lint run ./...` before
  proposing a PR; keep the README binaries table and badges accurate.
- **Ask first:** changing the Go version, renaming binaries/`cmd` dirs, altering the release archive
  layout (per-binary archives + raw binaries are intentional — see `.goreleaser.yml`), or editing CI.
- **Never:** introduce cgo, commit `dist/` or secrets, or skip the lint/test gates.

## Worktrees

This repo uses a bare-repo + typed-worktree layout managed by the `gt` CLI. See
`.claude/rules/worktree-per-session.md` (auto-loaded) and the `use-gt` skill — one session, one
`gt wt add <type/name>` worktree; never use raw `git worktree` or edit inside `.bare/`.
