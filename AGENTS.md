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

### Rotation and recovery (slice #110)

`inforge pki rotate <env> <name>` rotates one tier of a two-tier PKI:

- **`--leaf`** — no-op mint; prints guidance to run `inforge pki renew` (leaves are short-TTL; expiry is the only revocation mechanism).
- **`--intermediate <scope>`** — re-mints one scope's intermediate with a fresh key, signed offline by the cold root (`INFORGE_PKI_ROOT_KEY`). Regional boundary keeps the rotated intermediate invisible outside the rotated scope. Refused if a root overlap is active (`PreviousRoots` is non-empty) — finalize the root rotation first.
- **`--root`** — dual-root overlap: mint a new cold root, retain the old in `PKI.PreviousRoots`, re-sign every existing intermediate over its existing public key (`pki.ReSignIntermediate`). Old-key leaves keep verifying because the old root stays in the trust anchor set (`PKI.RootCerts()` = active root + previous roots). Run with `--finalize` to drop the retained root and end the overlap; both the begin step and the finalize step require the offline root identity.

`inforge pki recover-intermediate <env> <name> <scope>` handles a compromised intermediate: same fresh-key re-mint as planned rotation, but the operational urgency differs — all leaves the old intermediate signed must be replaced immediately (run `inforge pki renew` right after; do not wait for the daily timer).

Key internal seams introduced in slice #110:

- **`pki.ReSignIntermediate`** — re-issues an intermediate cert over its *existing* public key signed by a new root. Key is preserved; serial and validity window are refreshed. Used exclusively by `--root` rotation.
- **`intermediateTemplate`** (unexported) — shared x509 template builder used by both `GenerateIntermediate` (first-mint) and `ReSignIntermediate` (re-sign). Do not duplicate this template logic elsewhere.
- **`mintScopeIntermediate`** (unexported, `cmd/inforge`) — single shared path for decrypt-root → sign-intermediate → encrypt-to-CI. Used by `runPkiIntermediate` (first-mint) and `reissueIntermediate` (re-mint). Do not bypass it with a hand-rolled equivalent.
- **`PKI.PreviousRoots []Material`** — retained old roots during overlap (certs + cold keys). **`PKI.PreviousIntermediates map[string][]string`** — certs of old intermediates superseded during an overlap (audit trail; keys discarded). **`PKI.RootCerts() []string`** — returns active root cert + all previous-root certs; the full trust-anchor set mid-overlap.

### Host projection (slice #109)

- **`internal/hostpaths`** is the dependency-free (stdlib-only) source of truth for the on-host names
  both `inforge` and `inforge-bootstrap` must agree on byte-for-byte: `RuntimeSubdir`/`RuntimeDir`
  (the tmpfs PEM dir) and `UnitName`. It exists so the minimal static bootstrap binary can import it
  without pulling in the deploy-side packages (`internal/service` → `naming`/`types` → the Pulumi SDK).
- **`internal/bootstrapper.projectFiles`** is the single projection path, used by both the ExecStart
  boot path (`runBoot`) and the renewal timer (`runProject`): for each descriptor `files:` entry it
  fetches the provider key, writes the PEM into the service's tmpfs `RuntimeDir`
  (`/run/wardnet/<svc>`, dir mode `0700`, from the unit's `RuntimeDirectory=`), mode `0400` owned by
  the service user (chown'd **while still root**, before the privilege drop), and sets the `*_PATH`
  env var. Projection is an **atomic set**: every changed file is staged to a temp then renamed only
  after all stage cleanly, so a service never starts with a fresh leaf cert but a stale/absent key.
  It reports `changed` so the renewal path reloads only on a real rotation.
- **A mesh service's leaf is minted at release time, not only on the timer.** A service first runs on
  its first `inforge releases deploy`; since the boot path projects whatever the provider holds, that
  first start (and every update) would crash-loop until the daily timer fired. So `inforge releases
  deploy` mints the released service's leaf **before** the `systemctl restart`, reusing the shared
  `renewMeshCerts` core scoped to that one service. It runs from the infra repo, so it holds the same
  `INFORGE_SECRETS_KEY` as `inforge deploy`. Non-mesh services skip it.
- **The `MTLS_*_PATH` env names are reserved.** `inforge validate` rejects a service `environment:`
  key colliding with a `meshcert.DescriptorFiles()` name (projection would overwrite it), and rejects
  a multi-line `reload:` (it becomes one `ExecReload=` line).
- **Renewal is pull-based, per service.** A `wardnet-<svc>-renew.timer` runs `inforge-bootstrap project
  <dir>` daily; on a changed leaf it runs `systemctl reload-or-restart` — **reload** when the service
  declares `reload:` (emitted as `ExecReload=`, no downtime), else **restart**. `inforge pki renew` only
  writes the provider; hosts converge on their own. The renewal oneshot must NOT declare its own
  `RuntimeDirectory=` (systemd would delete the running service's dir when the oneshot stops).
- **A mesh service is provisioned even with no `environment.yaml`.** Deploy gives any `pki:` service a
  workspace + per-service identity (read scope on `/<svc>`, covering `/<svc>/mtls`) + `credential.age`,
  so the host can fetch its leaf. The skip is `len(svc.Environment)==0 && svc.Pki==""` in both
  `program.provisionServiceSecrets` and `infisical.ProvisionService`.

## Grants (#117, ADR-0025)

A **grant** is a service's declared, permissioned access to a **Grantable** resource, materialized as
the env vars it composes over the fields the resource publishes. It is authored as a `grants:` list on
the service manifest (topological, beside `pki:`/`ingress:` — **not** in `environment.yaml`), each entry
naming a target `<type>/<name>`, a permission (`ro`|`rw`), and an `outputs:` map of env-var → template
over `{FIELD}` placeholders. A grant *creates/issues* a credential (DB user, minted cert), distinct from
a `ref:` (which only reads an existing output) and from mesh `pki:` membership (intrinsic identity).

Landing in dependency order: slice A = grant core + schema + credential-free validation (#123);
**slice B (this code) = the Database Grantable** — `Database.Grant` mints a scoped per-service Postgres
role via the `neon:resources:NeonRole` plugin resource (Neon role API + pgx `GRANT`s as the DB owner,
CGO-free), and the credential-bearing `DatabaseOutputs.ConnectionURL` is **removed** (`ref:database/*`
is now rejected — DB credentials flow only through grants). slice C = the PKI resource Grantable
(sidecar + `inforge pki generate` + file projection); `PKIResource.Grant` stays a stub.

- **`ro`** = read-only (CONNECT/USAGE/SELECT); **`rw`** = read/write **plus** `CREATE ON SCHEMA public`
  (the service owns its own migrations). The role-provisioning capability rides on
  `types.DatabaseOutputs.RoleProvisioner` threaded through `AllOutputs` (so a regional service granting a
  `database/global/<name>` resolves the same way `ref:` does); the per-service role is named for the
  **consuming** service instance (`naming.Resource(env, consumerSlug, "dbrole", svc-db)`). The deploy
  wiring is `program.resolveDatabaseGrants` → `infisical.ProvisionService(…, grantSecrets)`, merging
  grant value secrets into the same `/<svc>/infra` batch. Bootstrapper untouched.

- **`internal/grant`** is the abstraction. `Grantable.FieldNames(perm) (values, files)` is
  **credential- and instance-independent** (keyed by resource *type* + permission) — the validator calls
  it on a zero-value Grantable from `grant.For(type)`, so grant validation is real without building the
  providers. `Database` publishes value fields `{USER,PASSWORD,HOST,PORT,DBNAME,URL}` (same for ro/rw;
  `{URL}` is the already-encoded connection URI — prefer it for DSN composition); a
  `PKIResource` publishes file fields `{CERT}` for verify (ro) and `{CERT,KEY}` for issue (rw).
  `ParseTemplate`/`Template.{Fields,HasLiteral,Interpolate}` are the shared `{FIELD}` machinery.
- **A value field** composes a string secret (the ADR-0010 env-secret path); **a file field** resolves
  to a projected PEM's on-host path (the descriptor `files:` path, slice #109). The two never mix in one
  template — a file field must stand alone. So the bootstrapper needs no new mechanism.
- **The PKI resource is its own declarative type** (`types.PKIResourceSpec`, `schemas/pkiresource.json`,
  `regional|global/pki/<name>/manifest.yaml`): **root-only**, scope derived from its folder. It is
  distinct from the mesh-auth `pki.enc.yaml` store (two-tier, `pki:` membership). The two never cross — a
  grant targets only a root-only PKI resource; `pki:` names only a two-tier mesh PKI. Slice A defines and
  validates the shape; the `pki.enc.yaml` sidecar + generate command are slice C.
- **`validate.checkGrants`** is the credential-free pass: target resolves to a supported Grantable of the
  right shape (`database/*`, `pki/*` root-only); permission ∈ {ro,rw}; every `{FIELD}` is published for
  that permission; a file field stands alone; output env names avoid the reserved
  `INFORGE_*`/`meshcert.DescriptorFiles()` names and don't collide with `environment.yaml` keys or each
  other across the service's grants. The cross-region boundary falls out of target resolution (shared
  regional set + `global/` prefix), exactly like `ref:`.

## CDN and App (Slice 1, schema only)

`cdn` and `app` are two new declarative resource types for front-end (React SPA) CDN deployment.
They are schema scaffolding only in this slice — deploy-time realization (Cloudflare Workers Static
Assets) and the release delivery adapter are later slices.

- **`CdnSpec`** (`types.CdnSpec`, `schemas/cdn.json`, `regional|global/cdn/<name>/manifest.yaml`) —
  the edge-platform resource: a sibling of network, not a workload. Its provider is **NOT** on the
  spec — it comes from the scope's `cdn` authority in `regions.yaml` (`regions[region].cdn.provider`
  or `global.cdn.provider`), exactly as derived DNS records take their provider from the dns
  authority. When any cdn or app resources exist in a scope the validator enforces that the scope has
  a cdn authority with a configured provider (mirroring the secrets-provider availability check).
- **`AppSpec`** (`types.AppSpec`, `schemas/app.json`, `regional|global/app/<name>/manifest.yaml`) —
  a static SPA workload: a sibling of service, **not** a service subtype. It references a `cdn` by
  name in the **same scope** (`cdn:` FK). `spa: true` enables the SPA deep-link fallback (404 →
  index.html). Like cdn, it carries no provider field — it inherits the cdn's provider, which inherits
  from the scope's cdn authority.
- **`regions.yaml` cdn authority** (`regions.CdnAuthority`) — optional block on a region and on the
  global block, parallel to `dns:`. When declared, `provider:` is required (an empty provider is
  rejected by `checkRegionsFile`).
- **`internal/loader`** — `NormalizeCdn` and `NormalizeApp` trim free-text fields; loader reads
  `cdn/` and `app/` sub-folders in both scopes alongside the existing resource folders.
- **`internal/validate`** — `checkApp` enforces the same-scope cdn FK (see rule
  `.agents/rules/app-cdn-fk-is-same-scope-only.md`); `cdnConsumerPaths` / `collectParsedPaths` are
  the generic helper for the provider-availability check; `schemaSet` now includes `cdn` and `app`.

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
