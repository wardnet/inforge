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
internal/pki/                                      # PKI store (pki.enc.yaml read/write), x509 helpers, leaf minting, ScopeGlobal const
internal/meshcert/                                 # deploy/renew orchestration: decrypt intermediate, mint leaf, compute trust set
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

## Rules

This repo has prescriptive rules in `.agents/rules/`. **Read every file in that directory before making changes here, and follow each rule strictly.**
Each file contains one rule. New rules go in that directory — one file per rule, kebab-case filename matching the rule's intent.

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

An environment may host several meshes; a service names the one it joins.

### Leaf minting (slice #108)

Deploy-time and renewal leaf minting is live. Key packages:

- **`internal/pki`** — `GenerateLeaf` mints a non-CA Ed25519 leaf (90-day TTL, clamped to parent) with a
  SPIFFE URI SAN; `SPIFFEID` builds `spiffe://<trustDomain>/<env>/<scope>/<service>`; `Store.TrustBundle`
  concatenates plaintext intermediate certs for a set of scopes.
- **`internal/meshcert`** — orchestration layer; `IntermediateSigner` decrypts the scope intermediate
  with the CI identity (`INFORGE_SECRETS_KEY`); `MintLeaf` / `MintServiceLeaf` sign a leaf from it;
  `TrustSet` computes the peer-verification scope set (global service → all regions + global; regional
  service → own region + global). Shared delivery constants: `MtlsDir`, `EnvLeafCertPath`,
  `EnvLeafKeyPath`, `EnvTrustBundlePath`, `CertFiles`, `DescriptorFiles`.
- **`providers/infisical.CertWriter`** — imperative (non-Pulumi) write path for `inforge pki renew`;
  authenticates once, caches workspace IDs; uses `workspaceName` / `servicePath` helpers shared with
  the Pulumi deploy path so renewed certs land at the same provider address deploy provisioned.
- **`internal/bootstrapper.Descriptor`** — `SupportedVersion` is now **3**; the `Files` map
  (`env-var → provider secret key`) carries mesh material paths. A descriptor with `files:` but no
  `provider.kind` is rejected.

`inforge pki renew <env>` is the CLI entry point: it mints one leaf per (service, scope) and writes
leaf cert + key + per-scope trust bundle to the secrets provider. It never runs the Pulumi program.
Schedule it separately from `inforge deploy` (e.g. cron).

### Host projection (slice #109)

- **`internal/bootstrapper.projectFiles`** is the single projection path, used by both the ExecStart
  boot path (`runBoot`) and the renewal timer (`runProject`): for each descriptor `files:` entry it
  fetches the provider key, atomically writes the PEM into the service's tmpfs `RuntimeDir`
  (`/run/wardnet/<svc>`, from the unit's `RuntimeDirectory=`), mode `0400` owned by the service user
  (chown'd **while still root**, before the privilege drop), and sets the `*_PATH` env var. It reports
  `changed` so the renewal path reloads only on a real rotation.
- **Renewal is pull-based, per service.** A `wardnet-<svc>-renew.timer` runs `inforge-bootstrap project
  <dir>` daily; on a changed leaf it runs `systemctl reload-or-restart` — **reload** when the service
  declares `reload:` (emitted as `ExecReload=`, no downtime), else **restart**. `inforge pki renew` only
  writes the provider; hosts converge on their own. The renewal oneshot must NOT declare its own
  `RuntimeDirectory=` (systemd would delete the running service's dir when the oneshot stops).
- **A mesh service is provisioned even with no `environment.yaml`.** Deploy gives any `pki:` service a
  workspace + per-service identity (read scope on `/<svc>`, covering `/<svc>/mtls`) + `credential.age`,
  so the host can fetch its leaf. The skip is `len(svc.Environment)==0 && svc.Pki==""` in both
  `program.provisionServiceSecrets` and `infisical.ProvisionService`.

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
