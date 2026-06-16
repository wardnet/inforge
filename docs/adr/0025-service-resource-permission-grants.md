# A service is granted a permissioned credential on a resource, materialized as env vars / files it composes

Issue #117. Spun out of the ADR-0024 amendment, which **removed** the two bespoke daemon service
fields (`issues:` / `verifies:`) on the grounds that exposing a PKI to a service *with a permission*
is one instance of a capability inforge did not have yet:

> **granting a service a permission on a resource, materialized as a credential/secret delivered to
> that service** — the same shape as "a service creating a user on a database and receiving its
> connection credential."

This ADR designs that general capability — the **Grant** — and lands its two first real consumers
together: a **Database** (per-service DB user) and a **PKI resource** (the daemon's standalone CA).
Building both at once is deliberate: a `Grantable` abstraction with a single implementation would be
the speculative generality inforge has burned itself on before; two concrete, dissimilar consumers
(stateful Pulumi user vs. stateless minted cert; value fields vs. file fields) are what make the
interface earn its keep.

## What a Grant is, and what it is not

A **Grant** is a service's declared, permissioned access to a **Grantable** resource. It is authored
as an entry on the service **manifest** (it is topological — it wires the service to a named resource
— so it sits with `pki:` and `ingress:`, not in the `environment.yaml` Source DSL):

```yaml
grants:
  - resource: database/main
    permission: rw
    outputs:
      TENANT_DB_URL: "{USER}:{PASSWORD}@{HOST}:{PORT}/{DBNAME}"
  - resource: pki/daemon
    permission: rw                 # rw == issue: the signing key
    outputs:
      DAEMON_CA_CERT_PATH: "{CERT}"
      DAEMON_CA_KEY_PATH:  "{KEY}"
```

It is **distinct from two things it superficially resembles**:

- **Not a `ref:`.** The Source DSL `ref:<type>/<name>.<output>` *reads* an existing output. A Grant
  *creates or issues* a credential — a DB user that did not exist, a cert minted for this service.
  The `ref:` mechanism stays, as the general way to reference **non-credential** outputs (a compute's
  public IP, a future resource's endpoint).
- **Not mesh `pki:` membership.** Membership (ADR-0024) is intrinsic identity: the service *is* a leaf
  of the mesh. A Grant is an *extrinsic permission* on a resource the service merely consumes. The two
  never mix — see "Two PKI concepts" below.

## Permission vocabulary: universal `ro` / `rw`

A Grant carries one of two permissions, `ro` or `rw`, and **each Grantable maps it to its own domain**:

| permission | Database | PKI resource |
|---|---|---|
| `ro` | read-only DB user | `verify` — the CA **cert** only (trust peers; cannot mint) |
| `rw` | read-write DB user | `issue` — the root **signing key** (mint certs online) |

The mapping is along a privilege gradient: `ro` is the consume/trust credential, `rw` is the
productive/dangerous one. For a PKI the signing key really is the resource's "write" — it brings new
certs into existence — and it is the credential to guard hardest, so `issue == rw` holds up. The
*materialized output* differs per type (a connection string vs. a cert path), but that is the
materialization layer, not the permission. One accepted cost: a reader sees `permission: rw` on a PKI
grant and must know it means "the signing key"; the per-type table above is the documentation for that.

Rejected: per-resource verbs (`issue`/`verify`, `read`/`write`) directly in the schema. Self-documenting
at the call site, but it makes the allowed enum vary by resource type and gives up the single
vocabulary the issue asked for.

## Fields and Outputs: the resource publishes raw material; the service composes its env surface

Granting produces a permission-dependent set of **fields** — named pieces of credential material.
Each field is one of two kinds:

- A **value field** — a string (DB `USER`, `PASSWORD`, `HOST`, `PORT`, `DBNAME`).
- A **file field** — material delivered as an on-host PEM file (PKI `CERT`, `KEY`).

The service does not consume fields directly. It declares **`outputs:`** — a map of *environment
variable → template* over `{FIELD}` placeholders, **scoped per grant** (so `{USER}` from
`database/main` and `{USER}` from `database/analytics` never collide). The resource publishes the raw
fields; the service decides whether that becomes one composed `DATABASE_URL` or four discrete vars.

Field kinds resolve differently, and that difference is what lets a Grant reuse **both** existing
delivery pipelines unchanged:

- A **value-field** template is composed at deploy time into a single string and written to the secrets
  provider as an ordinary secret — the ADR-0010 env-secret path. The bootstrapper injects it like any
  other env var.
- A **file-field** placeholder resolves to the projected PEM's **on-host path**. The PEM is written to
  the provider and projected to the service's tmpfs `RuntimeDir` via the existing
  `files:` / `projectFiles` mechanism (ADR-0024 / slice #109). The env var holds the path.

Consequently **`inforge-bootstrap` needs no new mechanism**: value outputs are secrets, file outputs
are the mesh `files:` path it already runs. A template that mixes a file field with anything else is a
validation error — a file field must stand alone (it is a path, not a substring).

## Lifecycle: materialized once at deploy, no renewal timer

All grant material is produced during `inforge deploy`, and **grants are not hooked to the per-service
renewal timer**. This is correct, not merely simple, because grant material is long-lived:

- A DB password is stable until rotated.
- A PKI-resource `verify` grant delivers the **root cert** (~10y); an `issue` grant delivers the
  **root signing key**. The daemon that holds the key mints its own short-TTL leaves at runtime —
  inforge delivers the long-lived root once.

Short-TTL re-minting remains a mesh-*membership* concern (`inforge pki renew` + the daily timer),
which Grants deliberately do not touch.

**Database grants are Pulumi-managed.** The per-service DB user is a Pulumi resource created during
deploy; removing the grant (or the service) drops the user on the next deploy — declarative drift and
cleanup for free, consistent with all other cloud state. Dropping a user revokes access, not data.

## Two PKI concepts, separated by being two different resources

The mesh-auth PKI and the grantable PKI are different concepts in different contexts, and #117 keeps
them apart **structurally**, not by convention:

- The **mesh-auth PKI** stays the special, env-root `pki.enc.yaml` store (two-tier), managed by
  `inforge pki`, consumed via `pki:` membership. Untouched by this ADR.
- The **PKI resource** is a new, full **resource type** — a folder
  `…/pki/<name>/manifest.yaml` peer to `database/`, **root-only** topology, the daemon's standalone CA.

Because it is a normal resource it obeys the normal scope/region model: a `regional/pki/<name>/` is
instantiated into every region; a `global/pki/<name>/` is region-less; and Grants obey the same
cross-region boundary as everything else — a regional service may grant on its own region's PKI
resource or a global one, **never another region's**.

The two concepts are mechanically prevented from crossing:

- A Grant may target **only** a PKI resource (root-only). A grant on a two-tier mesh PKI is a
  validation error.
- The `pki:` membership field may name **only** a two-tier mesh PKI. `pki:` on a root-only PKI is a
  validation error.

So a mesh signing key is never handed to a service via a grant, and the daemon root key never leaks
into the mesh-membership path.

### Realization and key custody of the PKI resource

The PKI resource's root key is sensitive, and ADR-0017's invariant is **git-only writes — deploy never
writes secrets**. So the material is **CLI-generated and committed**, exactly like the mesh store:

- `manifest.yaml` declares the shape (topology `root-only`, scope, validity).
- An `inforge pki generate <env> <name>`-style command generates the root CA and writes an
  age-encrypted `pki.enc.yaml` sidecar **in the resource folder** — the key encrypted to the **CI
  recipient** (`INFORGE_SECRETS_KEY`), the cert in plaintext. The operator commits it.
- `inforge deploy` only **reads** the sidecar. A `verify` grant delivers the plaintext cert; an
  `issue` grant decrypts the key with `INFORGE_SECRETS_KEY` and delivers it.

A **regional** PKI resource has **one independent root per region**, since the regional set is isolated
(no cross-region access) and a root-only topology has no shared intermediate to bridge regions: the
generate command runs per region and writes a region-scoped sidecar, so a regional service's `issue`
grant only ever yields its own region's signing key. A **global** PKI resource generates a single
region-less root. The example `pki/daemon` is global — one CA shared by the mesh.

The key is therefore deliberately **"warm"** — encrypted to the CI recipient and decryptable at
deploy. That is correct for an online signer, and is the explicit difference from the mesh **cold**
root (encrypted to an offline operator key, never held by CI). Anyone auditing the two stores should
read the recipient to tell them apart.

## A database stops exposing a credential-bearing output

Before this ADR, a service reached a database by referencing its **owner** `connectionUrl` (user +
password) through the Source DSL — handing the admin credential to every consumer. That is the exact
footgun per-service grants exist to remove. So:

- A **Database exposes no credential-bearing output.** `DatabaseOutputs.ConnectionURL` (with
  credentials) is removed.
- **DB credentials flow only through Grants** (a scoped per-service user). `ref:database/<name>.…`
  may reference only non-credential outputs.

This is a **breaking** change for any current `ref:database/*.connectionUrl` consumer; the DB-grant
slice migrates them.

## The `Grantable` interface

`Grant` resolution runs **inside the Pulumi program** during service provisioning
(`program.provisionServiceSecrets` / `infisical.ProvisionService`), where DB users are created and
secrets are written. The abstraction unifies *"fields for (service, permission)"*, not execution:

```go
// internal/grant
type Permission string // "ro" | "rw"

type Fields struct {
    Values map[string]pulumi.StringOutput // value fields: USER, PASSWORD, …
    Files  map[string]FileMaterial        // file fields: CERT, KEY (PEM to write + project)
}

type Grantable interface {
    Grant(ctx *pulumi.Context, service string, perm Permission, env, region string) (Fields, error)
    FieldNames(perm Permission) (values, files []string) // credential-free, for validation
}
```

`Database` (creates the `NeonRole`, applies the `ro`/`rw` `GRANT`s, returns value fields) and
`PKIResource` (reads the folder sidecar, returns file fields) implement it. The provisioner
interpolates each grant's `outputs:` over the returned `Fields`, emitting value secrets and descriptor
`files:` entries.

## Validation (credential-free, `inforge validate`)

- `resource` resolves to an existing Grantable of a supported type (`database/*`, `pki/*`).
- `permission ∈ {ro, rw}`.
- Every `{FIELD}` in an `outputs:` template is published by the target for that permission.
- A template mixing a file field with any other token → error.
- `pki/*` grant target must be **root-only**; `pki:` membership must be **two-tier**.
- Grant respects the cross-region boundary (regional service → own region or global; no cross-region).
- Output env-var names don't collide with reserved `INFORGE_*` / `MTLS_*` or with each other across the
  service's grants.

## Consequences

- **Net-new in the Neon provider:** the Neon API creates a *role* but not its RO/RW privileges — those
  are Postgres `GRANT`s run over a SQL connection as the owner. This pulls a pure-Go Postgres driver
  (`pgx`, CGO-free) into the provider and adds a connect-as-owner step. No cgo, so the static-binary
  invariant holds.
- **The bootstrapper is unchanged** — the single biggest payoff of the field-kind split.
- **Removing `connectionUrl` is breaking** and must migrate existing consumers.
- The capability is intentionally general but **lands with two real consumers**; a third Grantable
  (e.g. a future Kafka resource: `ro`/`rw` → an ACL'd principal, fields `{BROKERS, USERNAME,
  PASSWORD}`) implements `Grantable` without touching the DSL, the validator, or the bootstrapper.

### Implementation notes (slice B)

- **GRANT semantics:** `ro` = `CONNECT` + `USAGE` + `SELECT` on schema `public` (+ matching `ALTER
  DEFAULT PRIVILEGES`). `rw` = `ro` + `INSERT/UPDATE/DELETE` + sequence `USAGE/SELECT/UPDATE` **and**
  `CREATE ON SCHEMA public` — a `rw` service owns its own migrations (DDL), not just data. Statements
  are run as the database owner over a pgx connection inside the `neon:resources:NeonRole` plugin
  resource; identifiers are quoted via `pgx.Identifier.Sanitize()`.
- **`ref:database/*` is now rejected** (validate + the deploy-time `resolveRef`) with a "use a grants:
  entry" hint — a database has no referenceable output today. Regional access to a **global** database
  uses the same `global/` redirect grants and `ref:` share, and the per-service role is named for the
  **consuming** service instance (`wardnet-<env>-<consumerSlug>-dbrole-<svc>-<db>`) so two regions
  granting one global database never collide.
- **Threading:** the role-provisioning capability rides on `DatabaseOutputs.RoleProvisioner` through
  `AllOutputs` (not adapter-instance memory), because the global registry is not shared with the
  regional service loop. The `global/` redirect is the single shared `types.ResolveScoped` helper used
  by both `ref:` resolution and grant target resolution, so they cannot drift.
- **Published value fields:** `USER`, `PASSWORD`, `HOST`, `PORT`, `DBNAME` (literal/decoded values) plus
  **`URL`** — the role's full, already-URL-encoded connection URI. Compose a DSN with `{URL}`, not a
  hand-assembled `{USER}:{PASSWORD}@…`, which would not URL-encode a password containing reserved
  characters. Only one grant per resource target is allowed (a duplicate target would collide on one
  per-service role; `rw` already subsumes `ro`).
- **Role lifecycle:** the `NeonRole` resource ignores drift in the (transient) owner connection URI and
  API key, so a non-byte-stable Neon connection string never churns every consumer role. A `permission`
  change is a replace (drop + re-mint, rotating the role password) — acceptable for a short-lived
  per-service role, since `inforge releases deploy` restarts the unit and re-fetches. Delete is
  best-effort on the SQL cleanup (REASSIGN/DROP OWNED) and always proceeds to the control-plane role
  drop, so a suspended endpoint at destroy time does not wedge teardown.

## Status

Accepted. Slice A (grant core + schema + validation) shipped in #123; **slice B (Database Grantable,
this change) implements `Database.Grant` and removes `connectionUrl`**. Slice C (PKI resource Grantable)
remains a stub.
