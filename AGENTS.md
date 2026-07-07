# inforge — agent guide

Inforge is a Go toolchain that turns declarative infrastructure definitions into real deployments
via Pulumi and GitHub Actions. This repository builds two statically-linked binaries: the `inforge`
CLI and the `inforge-agent` on-host runtime agent (every service's systemd ExecStart). It builds no
Pulumi provider plugins of its own — the standard published plugins a project needs are fetched by
`inforge plugins install`.

## Commands

```sh
go build ./...                 # build all binaries
go test -race ./...            # run tests (race detector on, as CI does)
golangci-lint run ./...        # lint — must be clean before a PR
go run ./cmd/inforge           # run the CLI locally

# Release build dry-run (produces dist/ — two binaries × os/arch):
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
```

## Layout

```
cmd/inforge/                                       # the inforge CLI (user-facing)
cmd/inforge-agent/                             # on-host runtime agent (service ExecStart)
internal/agent/                             # agent core (descriptor, fetch, decrypt, exec)
internal/pki/                                      # PKI store (pki.enc.yaml read/write), x509 helpers, leaf minting, ScopeGlobal const
internal/meshcert/                                 # deploy/renew orchestration: decrypt intermediate, mint leaf, compute trust set
internal/validate/                                 # inforge validate — structural checks incl. credential-free PKI pass
internal/postgres/                                 # self-hosted Postgres render pkg (install/config/role/paths) — ADR-0036
internal/dbbackup/                                 # on-host per-database backup + restore renderers, R2 delivery — ADR-0036
providers/{hetzner,cloudflare}/                    # cloud provider Go SDK packages (NOT Pulumi plugins)
.goreleaser.yml                                    # build/release config (v2 schema)
.golangci.yml                                      # lint config (v2 schema)
.github/workflows/{ci,release}.yml                 # CI on PRs to main; release on v* tags
.github/dependabot.yml                             # gomod + github-actions, 5-day cooldown
```

- Module path: `github.com/wardnet/inforge`. Go directive: `go 1.25.8` (floored by the Pulumi SDK;
  CI/release build on Go 1.26).
- `inforge` builds no Pulumi provider plugins of its own. `inforge plugins install` downloads the
  standard published Pulumi plugins a project needs (e.g. `pulumi-random`) from their upstream
  releases; the Hetzner and Cloudflare integrations are Go SDK packages under `providers/`, not
  plugins. (The bundled Neon and Infisical plugins were retired by ADR-0036 and ADR-0035.)

## Resource naming convention

All cloud resource names follow `wardnet-<env>-<regionSlug>-<type>-<name>[-<NN>]`.

| Token | Example | Source |
|---|---|---|
| `wardnet` | fixed | `naming.usage` const |
| `env` | `prd` | environment name |
| `regionSlug` | `use1` | `regions.Table.Slug(region)` |
| `type` | `vm`, `fw`, `net`, `subnet`, `db`, `project`, `secrets`, `workspace`, `record`, `ingress`, `svc`, `identity`, `app` | resource type token |
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
- **Delivery (superseded by ADR-0035, see below).** `inforge pki renew`'s write path is no longer a
  provider client — it SSH-pushes an age-encrypted `leaf.age` directly to each host and signals
  reload-or-restart (`cmd/inforge/pki.go`, `cmd/inforge/sshpush.go`). There is no more
  `providers/infisical` package.
- **`internal/agent.Descriptor`** — the `Files` map (`env-var → key into the decrypted secrets/leaf
  blob`) carries mesh material paths.
- **Observability env-var contract (#134).** `agent.buildEnv` injects a non-secret OTel
  resource-identity set into every service (under the reserved `INFORGE_*` prefix):
  `INFORGE_SERVICE_NAMESPACE` (= service name, `service.namespace`), `INFORGE_INSTANCE_ID`
  (random per-(re)start, generated in `runBoot` via `newInstanceID`, `service.instance.id`),
  `INFORGE_DEPLOYMENT_ENV` (`deployment.environment.name`), `INFORGE_DEPLOYMENT_REGION_SLUG`
  (`region`), and `INFORGE_HOST_ID` (full VM resource name, `host.id`). The v4 bump **dropped**
  `Deployment.Namespace`/`INFORGE_DEPLOYMENT_NAMESPACE` and **renamed** the emitted env
  `INFORGE_DEPLOYMENT_ENVIRONMENT` → `INFORGE_DEPLOYMENT_ENV`; it **added** `Deployment.HostID`.
  The host id is built in `renderDescriptor` from the service's resolved compute key
  (`naming.Resource(env, slug, "vm", hostKey)`), so it matches the cloud server name and is stable
  across restarts.

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
  both `inforge` and `inforge-agent` must agree on byte-for-byte: `RuntimeSubdir`/`RuntimeDir`
  (the tmpfs PEM dir) and `UnitName`. It exists so the minimal static agent binary can import it
  without pulling in the deploy-side packages (`internal/service` → `naming`/`types` → the Pulumi SDK).
- **`internal/agent.projectFiles`** is the single projection path, used by both the ExecStart
  boot path (`runBoot`) and the renewal timer (`runProject`): for each descriptor `files:` entry it
  fetches the provider key, writes the PEM into the service's tmpfs `RuntimeDir`
  (`/run/wardnet/<svc>`, dir mode `0700`, from the unit's `RuntimeDirectory=`), mode `0400` owned by
  the service user (chown'd **while still root**, before the privilege drop), and sets the `*_PATH`
  env var. Projection is an **atomic set**: every changed file is staged to a temp then renamed only
  after all stage cleanly, so a service never starts with a fresh leaf cert but a stale/absent key.
  It reports `changed` so the renewal path reloads only on a real rotation.
- **Service-side mtls projection is opt-in: `mtls_files: true` (ADR-0033).** Only a service running a
  raw mTLS plane outside the mesh (e.g. tunneller's node↔node forward, wardnet-cloud ADR-0014)
  declares it; it keeps `/<svc>/mtls` provider writes, descriptor `files:`, the per-service renew
  timer, and the release-time mint (`inforge releases deploy` mints its leaf **before** the
  `systemctl restart`, via `renewMeshCertsAs` in per-service mode, so first start never crash-loops).
  Every other mesh member holds **no** cert material — the mesh proxy is the sole leaf custodian.
- **The `MTLS_*_PATH` env names are reserved.** `inforge validate` rejects a service `environment:`
  key colliding with a `meshcert.DescriptorFiles()` name (projection would overwrite it), and rejects
  a multi-line `reload:` (it becomes one `ExecReload=` line).
- **Renewal is push-based (ADR-0035, superseding ADR-0033's pull design).** There are no more
  on-host renewal timers: `inforge pki renew` mints a fresh leaf and SSH-pushes the host's
  `leaf.age` directly, then unconditionally signals `systemctl reload-or-restart` — **reload** when
  the service declares `reload:` (emitted as `ExecReload=`, no downtime), else **restart**. The mesh
  proxy's own leaf.age (aggregating every co-located service's leaf + trust bundle) is pushed and
  reloaded the same way. See "Secrets and mesh leaf delivery" below.
- **A service is provisioned only when it has something to fetch.** The skip is
  `len(svc.Environment)==0 && !svc.MtlsFiles` (+ no grants) in `program.provisionServiceSecrets` —
  `pki:` alone no longer forces anything to be delivered at deploy time: a plain mesh member's leaf
  is the mesh proxy's business, and an `mtls_files:` service's own leaf is `inforge pki renew`'s.

## Grants (#117, ADR-0025)

A **grant** is a service's declared, permissioned access to a **Grantable** resource, materialized as
the env vars it composes over the fields the resource publishes. It is authored as a `grants:` list on
the service manifest (topological, beside `pki:`/`ingress:` — **not** in `environment.yaml`), each entry
naming a target `<type>/<name>`, a permission (`ro`|`rw`), and an `outputs:` map of env-var → template
over `{FIELD}` placeholders. A grant *creates/issues* a credential (DB user, minted cert), distinct from
a `ref:` (which only reads an existing output) and from mesh `pki:` membership (intrinsic identity).

Landing in dependency order: slice A = grant core + schema + credential-free validation (#123);
**slice B = the Database Grantable** — `Database.Grant` mints a scoped per-service Postgres role on the
cluster host (ADR-0036 self-hosted minting: `postgres.MintRoleScript` run over `remote.NewCommand`
local peer auth, CGO-free — the retired Neon path minted it via a `neon:resources:NeonRole` plugin
resource), and the credential-bearing `DatabaseOutputs.ConnectionURL` is **removed** (`ref:database/*`
is now rejected — DB credentials flow only through grants). slice C = the PKI resource Grantable
(sidecar + `inforge pki generate` + file projection); `PKIResource.Grant` stays a stub.

- **`ro`** = read-only (CONNECT/USAGE/SELECT); **`rw`** = read/write **plus** `CREATE ON SCHEMA public`
  (the service owns its own migrations). The role-provisioning capability rides on
  `types.DatabaseOutputs.RoleProvisioner` threaded through `AllOutputs` (so a regional service granting a
  `database/global/<name>` resolves the same way `ref:` does); the per-service role is named for the
  **consuming** service instance (`naming.Resource(env, consumerSlug, "dbrole", svc-db)`). The deploy
  wiring is `program.resolveDatabaseGrants` merging grant value secrets into the same resolved
  plaintext map `deliverServiceSecrets` age-encrypts into the service's `secrets.age` (ADR-0035).
  Agent untouched.

- **`internal/grant`** is the abstraction. `Grantable.FieldNames(perm) (values, files)` is
  **credential- and instance-independent** (keyed by resource *type* + permission) — the validator calls
  it on a zero-value Grantable from `grant.For(type)`, so grant validation is real without building the
  providers. `Database` publishes value fields `{USER,PASSWORD,HOST,PORT,DBNAME,URL}` (same for ro/rw;
  `{URL}` is the already-encoded connection URI — prefer it for DSN composition); a
  `PKIResource` publishes file fields `{CERT}` for verify (ro) and `{CERT,KEY}` for issue (rw).
  `ParseTemplate`/`Template.{Fields,HasLiteral,Interpolate}` are the shared `{FIELD}` machinery.
- **A value field** composes a string secret (the ADR-0010 env-secret path); **a file field** resolves
  to a projected PEM's on-host path (the descriptor `files:` path, slice #109). The two never mix in one
  template — a file field must stand alone. So the agent needs no new mechanism.
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
  regional set + `global/` prefix), exactly like `ref:` — **except a cross-scope `database/global/<name>`
  grant is rejected** (ADR-0036): a self-hosted database is private to its scope (5432 opens to the host's
  own CIDR only), so a regional service cannot reach a global cluster. `pki/global/<name>` grants stay
  valid (file-projected, no network hop). This is enforced in `checkGrants`; `checkService` additionally
  rejects a co-located service backend bind in the reserved Postgres port range (`postgres.ClusterPort`).

## Ingress and App (ADR-0026)

`ingress` and `app` are the two declarative resource types for front-end (React SPA) deployment. Apps
are **origin-served by our own nginx** (Let's Encrypt, HTTP-01 — no CDN edge: free Cloudflare Universal
SSL can't cover deep app hosts), and `ingress` is a **standalone, shared proxy tier** that fronts both
services (slice B, live) and apps (slice C, live). See ADR-0026 (which supersedes the "realization-driven,
no host-level resource" parts of ADR-0015). Slice landing: A = schema; B = ingress realization +
service migration; C = app static serving + DNS + descriptor; **D (live) = app release delivery + CLI**.

- **Slice B — the type rename.** The inline per-service route struct is now `types.RouteSpec` and the
  ingress resource is `types.IngressSpec` (the slice-A `IngressResourceSpec` name is gone). `ServiceSpec`
  carries `Ingress string` (FK → ingress resource, same scope) **and** `Routes []RouteSpec` (the typed
  routes, formerly the `ingress:` array). YAML migration: a service's `ingress:` array became `routes:`,
  and a new `ingress: <name>` FK names the ingress that fronts them.
- **`IngressSpec`** (`schemas/ingress.json`, `regional|global/ingress/<name>/manifest.yaml`) —
  the shared proxy tier: a sibling of network, not a workload. It references a compute `host:` by name
  in the **same scope** (`host:` FK, exactly like `service.host`) and reuses that host's
  provisioning/firewall/SSH. Its provider is **NOT** on the spec — it inherits its host's. The
  nginx/routing config is **not** declared; it is derived at deploy from the services (slice C: apps)
  that reference the ingress.
- **Slice B realization (`program.go` + `providers/hetzner`):** nginx moves OFF the service host onto
  the **ingress host**. `ingressRoutesByHost` groups every referencing service's routes under the
  ingress's host; `realizeIngress` installs nginx there and proxies to each backend over loopback
  (co-located) or the backend's `ComputeOutputs.PrivateIP` (cross-host). `IngressRoute.Backend` is the
  resolved upstream address; the provider renders the config inside an apply over the cross-host private
  IPs. Service/ingress FQDNs (`<svc>.svc` + vanity) resolve to the **ingress host's** public IP.
  Firewall (`firewallPlanByHost` → `types.FirewallPorts`): ingress hosts open public `listen` ports
  (+`:80` for ACME); cross-host backends open `target` ports **only** to the network CIDR (see rule
  `.agents/rules/cross-host-route-requires-same-network.md`). Every host keeps public `:22`.
- **`AppSpec`** (`types.AppSpec`, `schemas/app.json`, `regional|global/app/<name>/manifest.yaml`) —
  a static SPA workload: a sibling of service, **not** a service subtype. It references an `ingress` by
  name in the **same scope** (`ingress:` FK). `spa: true` enables the SPA deep-link fallback (404 →
  index.html). Like ingress, it carries no provider field — it inherits the ingress's (its host's). The
  public FQDN is the clean dotted form `naming.AppFQDN` (`<subdomain>.<base>` global,
  `<subdomain>.<slug>.<base>` regional — **no env segment for a static env**, flatter than
  `ServiceFQDN`; the no-env-segment property is conditional — an ephemeral env inserts its slug
  identity segment, see ADR-0028 and the Ephemeral environments section below).
- **Slice C realization (`program.go` + `internal/nginx` + `providers/hetzner` + new `internal/app`):**
  the ingress nginx serves each referencing app from disk. `ingressAppsByHost` groups apps under their
  ingress's host as `types.IngressApp{Name,FQDN,Root,Spa}` (FQDN + `current`-symlink root pre-resolved);
  `nginx.Render(routes, apps)` adds one `server { listen 443 ssl; acme_certificate …; root <dir>/current;
  location / { try_files … } }` per app — `$uri $uri/ /index.html` when `spa`, else `=404` — sharing the
  one ACME issuer and the `:80` challenge/redirect server. `realizeIngress` now triggers on the **union**
  of route hosts and app hosts (`unionKeys`), so an **app-only ingress** (apps, no service routes) still
  installs nginx and provisions its cert. Firewall: an app ingress host opens public `80`+`443` (apps have
  no backend → no private rule). DNS: a grey-cloud A record per app (`naming.AppFQDN` → ingress host's
  public IP; `Proxied=false` so LE HTTP-01 reaches the origin), sharing `resolveIngressApps` with the
  firewall + nginx derivations so the three can't drift. `provisionApps` seeds a **placeholder bundle**
  (`internal/app.PlaceholderIndexHTML` at `<folder>/placeholder/index.html`) and points `current` at it
  **only when nothing already occupies it** — so a re-run never reverts a released app — so the server
  block + cert provision before the first release.
- **`internal/app`** is the app analogue of `internal/service`: pure on-host path scheme (`Folder` =
  `/srv/wardnet/app/<name>`, `CurrentPath` = `<folder>/current` — the nginx doc root the release path
  swaps; `BundleDir` = `<folder>/<sha>`; `SwapCurrentScript`/`GCReleasesScript` are the atomic-swap +
  GC contract, see rule `.agents/rules/use-atomic-current-swap-for-app-releases.md`) + the **app deploy
  descriptor** (`app.BuildDeployDescriptor` → exported as the `appDeployDescriptor` stack output:
  `{app, ingress_host_dns, deploy_path, fqdn, spa, ssh_user}` per region), the contract slice D's
  `inforge release app` resolves an app's ingress host/path/FQDN from.
- **Slice D realization (`cmd/inforge/release.go`):** `inforge release app <env> <name>` delivers an
  app bundle to its ingress host and atomically swaps the served root. The **delivery-adapter seam**
  (`deliverRelease`) is the workload-agnostic transport — resolve targets → fetch by SHA from the R2
  store → scp + apply on each host → record the per-env manifest. The **service** path
  (`inforge releases deploy`) is refactored behind it as adapter #1 (`serviceApplyScript`, behaviour
  unchanged); the **app** path is adapter #2 (`appReleaseScript`: extract into `<sha>` dir → atomic
  `current` swap → `nginx -t && reload` → GC old bundles beyond `app.KeepReleases`). `--bundle <dir>`
  packages + pushes a local SPA build first (app artifacts are namespaced under `app/<name>` in the
  store); `--rollback` re-points `current` at a SHA already on the host without re-fetching. The
  placeholder seed (`provisionApps`) now precedes the nginx reload via an explicit `DependsOn`.
- **SNI-preread coexistence + health tier (ADR-0027).** A `forward` (passthrough) route may now
  **share a listen port** with `tls-termination` routes/apps. When a port carries both (a "mixed"
  port), `nginx.Render` moves the public socket into `stream{}` with `ssl_preread`: a
  `map $ssl_preread_server_name` routes known SNIs to internal `127.0.0.1:<loopback> ssl proxy_protocol`
  terminators (the moved http servers) and the unknown SNI to the forward backend (the `default` — so
  **one forward per port**). `set_real_ip_from 127.0.0.1; real_ip_header proxy_protocol;` recovers the
  client address across the loopback hop. Loopback ports come from the reserved range
  `[nginx.LoopbackBase, +nginx.MaxMixedPorts)` (`internal/nginx/paths.go`); a co-located backend
  `target`/`health_probes_port` must avoid it (see rule
  `.agents/rules/reserve-loopback-range-for-preread-terminators.md`). Non-mixed ports render
  byte-identically to before. Forward exclusivity is now per-port **against other forwards only**
  (`validate.checkService` via `ctx.forwardUsersByHost`); `:80` is still forbidden to a forward.
- **Health probes (ADR-0027).** A service's optional `health_probes_port` (backend port) is surfaced
  through the ingress's own `health_probes_port` (public listener, **default 81**, same field name on
  both specs — see rule `.agents/rules/health-probes-port-semantics.md`). `nginx.Render` adds a plain-HTTP `http{}` server per health endpoint on that port,
  matched **strictly** by `server_name` (the service's `naming.ServiceFQDN`) and reverse-proxied to
  `backend:<health_probes_port>` — no `default_server`, so a wrong Host is 404; within a matched
  server only the service's declared `health_probe_paths` proxy (exact-match locations + 404
  catch-all — required ≥1 with the port, ADR-0034). `IngressHealth` is the
  derived per-host entry (`program.ingressHealthByHost`), backend-resolved like a route (cross-host
  substituted in the provider apply). Firewall (`firewallPlanByHost`): the ingress host opens the
  public health port (`0.0.0.0/0`) when ≥1 referencing service declares one; a cross-host backend opens
  its `health_probes_port` privately to the network CIDR. `Render(routes, apps, health, healthPort)` and
  `IngressProvider.Realize(... health, healthPort ...)` carry the new arguments; `resolveIngressServices`
  now also admits health-only services (route-less).
- **Exposed ports (ADR-0029).** A service's optional `exposed_ports` (`[]types.ExposedPort{Proto,Port}`)
  are ports inforge opens on the host's **private-network CIDR only** — never the public internet, with
  **no ingress and no nginx**. They are the private sibling of `compute.firewall.inbound` (which is
  public). Realization: `firewallPlanByHost` reads them from **every** service directly (not via
  `resolveIngressServices`, so a private-only service with no ingress is included), collects them onto
  the service's own host as `FirewallPorts.PrivateExposed` (proto-aware, deduped, sorted), and the
  Hetzner `ensureFirewall` opens each scoped to `PrivateSourceCIDR` (reusing the cross-host-target
  private path; the TCP-only `addTCP` was generalized to `addRule(proto,…)`). They never touch
  `publicSources` — see rule `.agents/rules/exposed-ports-are-private-only.md`. Validation
  (`checkService`): no ingress required; a tcp exposed port shares the int-keyed (implicitly-TCP)
  `targetUsersByHost` space with route targets + health (must differ from the service's own route
  targets/health, a public listen port nginx holds on the host, another service's backend port, and —
  when the host runs an ingress — the reserved loopback range); a udp exposed port lives in the new
  proto-aware `udpExposedUsersByHost` and collides only with another udp exposed port; a duplicate
  `(proto,port)` on one service is rejected.
- **`internal/loader`** — `NormalizeIngress` and `NormalizeApp` trim free-text fields (and
  `NormalizeIngress` defaults `health_probes_port` to 81); loader reads `ingress/` and `app/`
  sub-folders in both scopes alongside the existing resource folders.
- **`internal/validate`** — `checkIngress` enforces the same-scope `host:` FK (single-instance vm,
  `global/` rejected), unique ingress names, **one ingress per host** (`ingressNamesByHost` — see rule
  `.agents/rules/one-ingress-per-host.md`), and health-port collision checks (must not be 80, must not
  equal a route listen port, must stay out of the loopback reserved range); `checkApp` enforces the
  same-scope `ingress:` FK (see rule `.agents/rules/app-ingress-fk-is-same-scope-only.md`) and unique
  app names/subdomains; `schemaSet` now includes `ingress` and `app`. **Forward exclusivity** is now
  per-port against other forwards only (`forwardUsersByHost`; a forward may coexist with
  tls-termination on the same port). **Cross-host same-network check** is hoisted outside the
  per-route loop so a health-only service (no routes, just `health_probes_port`) is also covered (see
  rule `.agents/rules/cross-host-route-requires-same-network.md`). There is **no** cdn authority or
  dedicated availability pass — an ingress inherits its compute host's provider, already covered by
  the compute provider-availability check. **Name uniqueness is enforced generically:** `validateType`
  (the single validation pass used for every resource type) rejects a duplicate `name:` within a scope
  for all types — network, compute, database, service, pkiresource, ingress, app. When adding a new
  resource type, pass a name-extractor func as the third argument to `validateType`; forgetting it
  would bypass the uniqueness check. **`host:` FK resolution for compute-backed resources**
  (`service.host`, `ingress.host`) is centralized in `resolveComputeHost(host, noun, ctx)` — see rule
  `.agents/rules/use-resolve-compute-host-for-host-fk.md`.

## Ephemeral environments (ADR-0028)

`inforge ephemeral up | down | reap` (alias `eph`) spins up, tears down, and reaps **ephemeral
(preview) environments**: TTL-bounded, network-segregated clones of a source env's *definition*,
deployed under a distinct generated slug identity, running the exact service/app SHAs live in the
source. The grain is create-and-destroy (Hetzner bills a server until it is deleted, even powered off).

- **Identity is decoupled from config source.** `program.Run` reads two stack-config values: the
  identity `environment` (= the slug — every name, FQDN, label, SPIFFE scope) and the config-source
  `source_environment` (the `resources/<src>/` tree, secrets, and `pki.enc.yaml` the loaders read).
  `source_environment` defaults to `environment`, so a static env is byte-for-byte unchanged. **Only**
  the loaders / secret-decrypt / PKI-store path switch to `source_environment`; everything else keeps
  using the slug. The mesh-cert path mirrors this split via `renewMeshCertsAs(configEnv, identityEnv)`.
- **`naming.AppFQDN` takes an `ephemeralSlug`** (ADR-0028 exception). A static env passes `""`
  (URLs unchanged); an ephemeral env passes its slug, inserted after the subdomain
  (`<sub>.<slug>.<base>` / `<sub>.<slug>.<region>.<base>`) so the clone never collides with the source's
  app hostname. The flag/slug is threaded to `resolveIngressApps`/DNS/nginx/cert so all three agree.
- **Hetzner labels** carry `ephemeral=true` + `expires_at` (epoch seconds — label values forbid `:`),
  via `tags.Ephemeral` threaded `BuildRegistry → hetzner.New/NewCompute`. The labels are for orphan
  **auditing only** — the reaper classifies from stack config, never from labels.
- **`up`** = provision (Pulumi up under the slug) + replicate-deploy (no service/SHA args): for every
  source service/app it reads `LoadManifest(name, source_environment)`, resolves each ephemeral host's
  source counterpart (env-label swap on host DNS, `sourceHostDNS`), and delivers that host's SHA via the
  existing `deliverRelease` path, writing `manifest.<slug>.yaml`. Per-host faithful; skip-and-reports a
  workload not deployed in the source rather than failing `up`.
- **`reap`** is three-signal, no confirmation: reap iff stack-config `ephemeral == "true"` AND
  `expires_at` is past (the pure decision is `reapDecision`). Both are written only by `up`, so no
  permanent stack can match. A missing or unreadable `expires_at` on an otherwise-ephemeral stack
  triggers a **fail-safe reap** (the stack is a disposable preview; letting it run forever leaks
  billing). A stack where the Pulumi config itself can't be read is warned and skipped, not reaped.
  Destroys by default; `--dry-run` lists only.
- **State backend is a hard requirement** (`requireObjectBackend`): the ephemeral commands need an
  `r2`/`s3` backend (per-stack object keying + `ListStacks` enumeration) and hard-fail on
  `git-branch`/`file`.
- **Network segregation** is a structural invariant — never peer Networks or share one across envs;
  see `.agents/rules/ephemeral-network-segregation.md`.

## Observability (ADR-0030, ADR-0031)

Two coupled pieces give Grafana Cloud cloud/host context. Both stamp the **same**
resource-attribute set so VM metrics and app telemetry correlate on `host.id`.

- **Resource-attribute enrichment (ADR-0030).** Beyond the #134 set, inforge injects four
  more OTel attributes where it is the sole authority: `cloud.provider`
  (`INFORGE_CLOUD_PROVIDER`), `cloud.region` (`INFORGE_CLOUD_REGION` = Hetzner `network_zone`),
  `cloud.availability_zone` (`INFORGE_CLOUD_AVAILABILITY_ZONE` = Hetzner `location`), and
  `host.type` (`INFORGE_HOST_TYPE` = server-type SKU). They are **provider-supplied** plain-string
  fields on `types.ComputeOutputs` (`CloudProvider/CloudRegion/AvailabilityZone/MachineType`),
  populated by `hetzner.Create()` (plan-time constants, no apply), read off the host in
  `renderDescriptor`, carried in `agent.Deployment`, and emitted by `buildEnv`
  **omit-if-empty** (a provider that doesn't supply one emits nothing). `host.name`/`os.type`
  were deliberately dropped (self-detectable by the process). This bumped the descriptor to
  **v5** (the strict `KnownFields` decoder makes any field addition a major bump). The consumer
  side is a four-row addition to the `(attribute, env_var)` table in wardnet-cloud
  `crates/common/src/telemetry.rs::resource()`.
- **Host VM-metrics collector (ADR-0031).** `internal/otelcol` (pure, Pulumi-free, like
  `internal/nginx`) renders an off-the-shelf **OTel Collector Contrib** config (`hostmetrics` →
  `otlphttp`) and the idempotent install shell (download the version-pinned `.deb`, verify the
  release checksum, `apt-get install` the local file keeping our config on upgrade). The
  `process` scraper is **off** so the agent runs **unprivileged** as the `.deb`'s `otelcol-contrib`
  user. `program.provisionObservability` is an **always-on** per-host pass **gated on env-level
  config**: `variables.yaml` `observability.otlp_endpoint` (non-secret) + the OTLP Basic-auth
  credential in `secrets.enc.yaml`. That credential is an inforge **reserved secret**, NOT a
  service container secret: it lives under the store's `reserved:` namespace as
  `observability/otlp_auth` (`otelcol.AuthSecretNamespace`/`AuthSecretKey`), is written with
  `inforge secret set <env> observability otlp_auth --reserved`, and is read directly by the deploy
  (`program.decryptReservedSecret`) — decoupled from the `vault:` service-secret path, so it
  surfaces even when no service uses `vault:`, and a user service may use the container name
  `observability` without colliding (see rule `reserved-secrets-live-outside-container-namespace`).
  With no endpoint it is a no-op; with an endpoint but no credential it fails the deploy. The
  credential is base64'd, `pulumi.ToSecret`-wrapped (encrypted in state), written `0600` owned by
  the collector user, and referenced from the config via the collector's `${file:…}` provider
  (never inlined). The config stamps the ADR-0030 attribute set + `host.id`.

## East-west service mesh (ADR-0032)

The mesh is the **east-west** plane (service↔service), **derived** — no resource. It materializes a
**second per-host nginx** (the mesh proxy, separate from the north-south ingress) on every host running
≥1 `pki:` service. Authoring surface is only the per-service `mesh:` block (`port` + callee-side
`allowed_services` + the ADR-0034 endpoint surface `public_paths`/`internal_paths` — absolute path
globs, ≥1 required across both lists, `pathglob`-validated, public↔internal overlap rejected);
everything else is generated, exactly like the ingress nginx. The callee's mesh proxy admits ONLY
declared paths (`public ∪ internal`, threaded into `meshnginx.LocalService.Paths` as regex
locations); an undeclared path is a JSON 404 even for an allowed peer. `public_paths` additionally
feed the gateway's derived routing table (rule `gateway-routes-are-derived-from-public-paths`).

- **Pure derivation (`program/mesh.go`, tested).** `expandAllowedCallers` projects a callee's authored
  `allowed_services` onto the concrete caller identities (`<scope>/<service>`) its local mesh admits —
  the **security-critical** step (regional callee → same-region + own gateway; global callee → a
  regional caller from every region, a global caller global-scoped). `meshInputsByHost` groups a scope's
  pki services per host into the renderer's `Local` (callees, one per `mesh:` block) + `Egress` (callers,
  every pki service) planes, assigning each a deterministic loopback egress port
  (`meshpaths.EgressPort(idx)` over the host's sorted services). `meshEgressPortsByService` recomputes
  that same assignment for the descriptor URL (they MUST agree — same group+sort). `meshTargets` builds
  the scope-wide routing table (host-global): every callee reached at its host's **private** IP, plus —
  for a regional scope only — every global callee reached at its global host's **public** IP (the
  cross-scope hop). A name in both scopes resolves same-scope.
- **Realization (`realizeMesh`, mirrors `realizeIngress`).** Per mesh host it resolves the listen
  address (host private IP regional / `0.0.0.0` global) and each target's `Addr` (`<ip>:MTLSPort`) inside
  a `pulumi` apply over compute IPs, renders `meshnginx.Config`, and installs the second nginx via
  `providers/hetzner.HetznerMesh` (`registry.Mesh`): unit `meshpaths.UnitName`, config
  `meshpaths.ConfigPath`, pid `meshpaths.PIDPath`. The global slice realizes first, so a regional scope's
  cross-scope targets see the global public IPs. `internal/meshnginx.UnitFile`/`SeedScript` add the
  systemd unit + a **placeholder-cert seed** (self-signed leaf/key/bundle per SNI, only-when-absent —
  the `provisionApps` idiom) run as an `ExecStartPre`, so `nginx -t` is green and the proxy starts
  before real leaves land.
- **Real leaf delivery is PUSH-based (ADR-0035, superseding ADR-0033's pull design and the custody
  shift it made).** There is no mesh workspace, no per-host provider identity, and no
  `credential.age` any more. Deploy writes only the on-host **mesh descriptor**
  (`agent.MeshDescriptor`, `MeshSupportedVersion=2`, strict fields, secret-free) to
  `meshpaths.AgentDir` (`program.deliverMeshHost`). `inforge pki renew` mints the per-host
  aggregate (each co-located service's leaf + one concatenated trust bundle, grouped by the shared
  `internal/meshplan.ServicesByHost` derivation, rule `mesh-host-grouping-is-single-sourced`),
  age-encrypts it to the host's own SSH host public key (read live over SSH), and SSH-writes it
  directly to `meshpaths.LeafPath` (`leaf.age`) before signaling
  `systemctl reload-or-restart wardnet-mesh` over the same connection (`cmd/inforge/pki.go`,
  `cmd/inforge/sshpush.go`) — zero Pulumi, still, and per-host/per-service failures accumulate
  rather than aborting the run. On the host, nginx cannot decrypt age itself: the mesh unit's
  `ExecStartPre` runs `inforge-agent mesh-project <dir>`, now a purely **local** decrypt of
  whatever `leaf.age` already sits on disk (no network, no retry loop) that projects each file into
  the tmpfs `RuntimeDir` (owner nginx, 0400) — `-`-prefixed so an absent/corrupt leaf.age (first
  boot, before the first push) never blocks the proxy start; the placeholder seed fills any
  remaining gap. There is no on-host renewal timer any more — a reboot's ordinary boot flow
  (local decrypt → project → start) is the self-heal. The deploy baseline
  (`cmd/inforge.meshBaseline`, post-`up` in `deploy`/`ephemeral up`) resolves the SSH key up front,
  then calls the same mint-and-push core (`renewMeshCertsAs`) directly — there is no separate
  "trigger" step. The provider-key ↔ on-host-path scheme is single-sourced in `meshpaths`
  (`LeafCertKey`/`LeafKeyKey`/`BundleKey`; `RuntimeDir + key == LeafCertPath`).
- **Firewall (`firewallPlanByHost(res, meshPublic)`).** A mesh host opens `meshpaths.MTLSPort` to its
  private network CIDR (regional) or `0.0.0.0/0` (the global host — the cross-scope mesh gateway; this is
  what structurally keeps regional meshes private). `meshPublic` is `region == globalScope`, threaded
  from `createInfra`.
- **Descriptor v6 (`internal/agent`).** `SupportedVersion` bumped 5→6 for the new `Descriptor.Mesh`
  block. `renderDescriptor` sets it for every `pki:` service; `buildEnv` injects `INFORGE_MESH_URL`
  (`http://127.0.0.1:<egress port>`), `INFORGE_MESH_SCOPE` (region name / `global`), and
  `INFORGE_MESH_PORT` (a callee's `mesh.port`, omitted for an egress-only member). These are the env
  vars wardnet-cloud reads (plain-HTTP in/out, `X-Mesh-Target` addressing, `X-Service-Identity` demux).

## North-south gateway realization (ADR-0032 daemon edge, ADR-0034 derived routes)

The `gateway` resource (authored: `host` + **`pki`** + `subdomain` + **`services:[names]`** +
optional `health_probes_port`/`health_probe_paths`; scope singleton; validated in `checkGateway`
incl. listed-service `allowed_services` + **same-`pki:`** match) is realized in two halves:

- **Edge half (north-south nginx).** The routing table is **derived, never authored** (ADR-0034):
  `toGatewayNginxRoutes` emits one `IngressGatewayRoute{Pattern, Service}` per (listed service,
  `mesh.public_paths` glob); `internal/pathglob` is the single glob syntax/compile/overlap source
  (`*` = one segment, trailing `/**` = node + tail). `program.gatewaysByHost` derives
  `types.IngressGateway` (FQDN = `naming.AppFQDN(subdomain,…)` — the same call feeding the DNS A
  record and the SNI-collision guard, so cert/record/demux can't drift); `realizeIngress`'s host
  union includes gateway hosts (a gateway-only host still installs nginx);
  `nginx.Render(routes, apps, health, healthPort, gateways)` emits one ACME server block per gateway
  with a `location ~ <compiled regex>` per derived route: WS-capable (`$connection_upgrade` map),
  path-preserving (target named out-of-band in `X-Mesh-Target`), Authorization forwarded untouched,
  XFF stamped, `proxy_pass http://127.0.0.1:<GatewayEgressPort>`, plus `location =` self-health
  blocks (`health_probe_paths` → `200 "ok"` on 443, edge liveness over the real daemon TLS path);
  unmatched → **JSON 404** at the edge. Cross-service glob overlap is rejected at validation — the
  load-bearing routing invariant (rule `gateway-routes-are-derived-from-public-paths`). A gateway on
  `:443` joins ssl_preread mixed ports exactly like an app. Firewall: gateway host opens public
  `443`+`80` (+ its health port when a listed service declares health). **Gateway health tier:** an
  ingress-less listed service's `health_probes_port` is surfaced on the gateway host (Host-demuxed,
  `GatewaySpec.EffectiveHealthProbesPort()`, default 81) with its ServiceFQDN A record at the
  gateway host — single-sourced in `resolveGatewayHealthServices` (nginx + firewall + DNS); a
  service with an ingress keeps its health there (D12). Health listeners everywhere serve only the
  service's declared `health_probe_paths` (exact match; required with the port — closed by default).
- **Mesh-client half.** The gateway is the synthetic mesh member `meshpaths.GatewayMember`
  ("gateway", reserved — `checkService` rejects the service name) with the FIXED
  `meshpaths.GatewayEgressPort` (= `EgressBase+MaxServices`; `InReservedEgressRange` covers it) —
  never in the positional `EgressPort(index)` sort (see rule
  `gateway-mesh-slot-is-fixed-and-name-reserved`). `meshplan.GatewayMemberByHost` is the
  single-source grouping (rule `mesh-host-grouping-is-single-sourced`): `meshInputsByHost` appends it
  as one extra egress caller (a gateway-only host gets an egress-only mesh proxy — `realizeMesh` +
  `deliverMeshHost` trigger there too), the renew core mints `CN=<scope>/gateway` from the gateway's
  `pki:` into its host's `/<hostKey>/gateway/leaf.*` aggregate, and the mesh descriptor/seed cover it
  via the shared egress-names flow. The gateway's leaf rides the ADR-0033 pull like every member.

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
