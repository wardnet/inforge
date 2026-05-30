# inforge — agent guide

Inforge is a Go toolchain that turns declarative infrastructure definitions into real deployments
via Pulumi and GitHub Actions. This repository builds three statically-linked binaries: the `inforge`
CLI and two Pulumi provider plugins (`pulumi-resource-neon`, `pulumi-resource-infisical`).

## Commands

```sh
go build ./...                 # build all binaries
go test -race ./...            # run tests (race detector on, as CI does)
golangci-lint run ./...        # lint — must be clean before a PR
go run ./cmd/inforge           # run the CLI locally

# Release build dry-run (produces dist/ — three binaries × os/arch):
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
```

## Layout

```
cmd/inforge/                                       # the inforge CLI (user-facing)
providers/neon/cmd/pulumi-resource-neon/           # Pulumi provider plugin — Neon
providers/infisical/cmd/pulumi-resource-infisical/ # Pulumi provider plugin — Infisical
.goreleaser.yml                                    # build/release config (v2 schema)
.golangci.yml                                      # lint config (v2 schema)
.github/workflows/{ci,release}.yml                 # CI on PRs to main; release on v* tags
.github/dependabot.yml                             # gomod + github-actions, 5-day cooldown
```

- Module path: `github.com/wardnet/inforge`. Go directive: `go 1.23`.
- The two `pulumi-resource-*` binaries are Pulumi plugins, **not** user commands. They are installed
  automatically by `inforge plugins install`, which downloads only the providers a project needs from
  the matching GitHub release. Users never invoke them directly.

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
