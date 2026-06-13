# PKI material is minted from cold-rooted trees in CI and delivered through the provider; a service host never holds a sign-capable key

The Wardnet bridge is being split into three separately-deployed services — Tenant (global),
DDNS (regional), and Tunneler (regional) — and every hop between them is secured by mTLS from
day one. That split happens in the app repo (#610); **inforge's job is only to mint the
certificate material and to inject it.** inforge is the issuance and secret-distribution layer,
not the consumer: rustls wiring, client-identity loading, and the Tenant's online CSR endpoint
all live in #610.

There are two independent trust trees, and every verifier trusts both roots:

1. **Service-mesh PKI** — a cold global root signs one intermediate per active scope; inforge
   mints a short-lived leaf per service instance from that scope's intermediate at deploy. No
   online CA serves leaves on demand; short TTL is the revocation mechanism.
2. **Daemon PKI** — a separate root. inforge does **not** mint its leaves (the Tenant issues
   them online). The root *key* goes to the Tenant only; the root *cert* goes to everyone as a
   verify-only anchor.

The guiding rule that falls out of this: **distribute root *certs* freely; guard root and
intermediate *keys* fiercely.** A service host never holds a key that can sign a mesh
certificate. CI never holds the cold root key at all.

## Relationship to ADR-0010 and ADR-0017 (read this first)

This ADR adds no new delivery mechanism and no new at-rest secret on the host. It composes two
existing ones:

- **Delivery rides ADR-0010 (runtime secret fetch) unchanged.** Cert material is written to the
  secrets provider (Infisical), scoped per service identity, exactly like every other secret.
  `inforge-bootstrap` fetches it at process start and projects it to a tmpfs PEM file. The
  durable source of truth is the provider; the PEM file is a per-boot projection, re-fetched
  after every reboot. The only standing secret on host disk remains `credential.age` — the
  least-privilege machine-identity credential ADR-0010 already established.
- **Storage and encryption reuse ADR-0017 (git-native encrypted store).** The PKI store is the
  structural twin of `secrets.enc.yaml`: a git-committed `*.enc.yaml`, age X25519, keys
  encrypted to a committed recipient, decrypted in CI with an identity from an environment
  variable. The CLI writes git only; the provider is written exclusively by `inforge deploy` on
  merge — the same single-writer invariant 0017 establishes, so a deploy can never roll back
  material the provider already serves.

An earlier draft sealed the mesh material into a host-key-encrypted `mesh.age` blob on disk.
**That is rejected here.** The established 0010/0017 rationale is that a hosted VM is a
higher-risk store than the provider: an at-rest blob gives no fetch audit and forces a re-key of
everything on a host-key leak. Cert material is a secret like any other, so it follows the same
path. Consistency with 0010/0017 wins.

The per-scope intermediate model builds directly on **ADR-0013** (global resources and
cross-reference direction) and **ADR-0023** (global placement region): "global" is a first-class
scope alongside the abstract regions, and an intermediate exists per active scope.

## Decisions

- **PKI is a first-class concept: a general store of N named PKIs.** `inforge pki` mirrors
  `inforge secret`, and `resources/<env>/pki.enc.yaml` mirrors `secrets.enc.yaml`. The mesh and
  the daemon are two named entries in that store, not two hard-coded special cases — adding a
  third PKI later is a store entry, not a code change.

- **The store has two recipients.** `rootRecipient` is an offline, operator-held age key; cold
  root keys are encrypted to it, and **CI never holds the matching identity**, so CI literally
  cannot sign with a cold root. `recipient` is `INFORGE_SECRETS_KEY` (the CI master, as in
  0017); intermediate keys and root-only issuer keys are encrypted to it and used at deploy.
  Certificates are stored inline as **plaintext PEM** (they are public); private keys are stored
  as **age ciphertext**. The store's *structure* — PKI names, topology, which scopes have
  intermediates, and all certs — is readable with no key at all, so `inforge validate` checks
  `pki:`/`issues:`/`verifies:` references against it exactly as it checks `vault:` against the
  secret store today, without decrypting anything.

- **Two topologies, derived from scope and from whether inforge mints the leaves.**
  - **`two-tier`** — a cold (offline) root plus **one intermediate per active scope** (the
    global scope *and* each region). Intermediate keys are CI-accessible; the root stays fully
    cold. inforge mints leaves from the *service's scope* intermediate at deploy. This is the
    **service mesh**. The global intermediate is mandatory: the cold root cannot sign at deploy,
    so the Tenant (global) leaf must come from a global intermediate.
  - **`root-only`** — a root, no intermediate, and no inforge leaf minting. The root key is
    delivered to a designated online issuer. This is the **daemon** PKI.

- **Delivery is ADR-0010 runtime-fetch, projected to tmpfs.** At deploy, the Pulumi `program`
  decrypts the service's *scope* intermediate key with `INFORGE_SECRETS_KEY`, mints a
  short-TTL leaf cert + key, and writes the leaf plus the relevant root cert(s) — and, for the
  daemon issuer, the daemon root *key* — into the provider under that service's existing scoped
  path. The on-host descriptor (ADR-0010) gains a `files:` map (path-env-var → provider key)
  alongside its `env:` map.

- **Per-boot projection via systemd `RuntimeDirectory`.** `inforge-bootstrap` fetches the
  designated cert secrets and writes PEM files atomically (write-temp + rename) into a
  RAM-backed `RuntimeDirectory=` directory, mode `0400`, owned by the service user, before it
  execs the service; it sets the `*_PATH` env vars to those file paths. A reboot wipes `/run`,
  and the bootstrapper re-fetches and re-projects on the next start — the PEM file is never a
  durable artifact.

- **A concrete three-field service DSL with derived identity.** A service references PKIs by
  name, the way it references its `host:`. The identity (CN/SAN) is *derived* from the service
  name and its scope (`tenant`, `ddns.<region>`, `tunneler.<region>`), never written by hand:
  - `pki:` (required) — this service's mTLS leaf identity (a mesh member).
  - `issues:` (optional) — names a `root-only` PKI this service issues online (the daemon root
    key + cert are delivered to it).
  - `verifies:` (optional) — names a `root-only` PKI this service only verifies against (the
    daemon root cert is delivered, verify-only).

  These are scalar today (a service issues or verifies at most one PKI); they can become lists
  later without breaking existing manifests. A generic `AssetSpec`-style schema is deliberately
  avoided in favour of these three concrete fields.

- **The env/path contract is authoritative here and shared verbatim with #610.** Each field maps
  to a fixed set of env vars naming the projected PEM paths:

  | Field | Material delivered | Env/path vars |
  |---|---|---|
  | `pki:` (required) | leaf cert + key, mesh root cert | `MTLS_LEAF_CERT_PATH`, `MTLS_LEAF_KEY_PATH`, `MTLS_MESH_ROOT_CERT_PATH` |
  | `issues:` (optional) | daemon root key + cert | `DAEMON_CA_KEY_PATH`, `DAEMON_CA_CERT_PATH` |
  | `verifies:` (optional) | daemon root cert (verify-only) | `MTLS_DAEMON_ROOT_CERT_PATH` |

- **Validation rules.** `pki`/`issues`/`verifies` must each name an existing PKI in the store; a
  `two-tier` service's scope must have an intermediate; `issues` requires a `root-only` PKI and a
  single, global issuer (the online signer is a singleton). All checks run against the store's
  plaintext structure, credential-free, at `inforge validate` time.

- **Pure-Go `crypto/x509` only.** All binaries are `CGO_ENABLED=0` static, so PKI uses stdlib
  `crypto/x509` plus `crypto/ed25519`/`ecdsa` — no openssl shell-out, no cgo. There is **no leaf
  CRL or OCSP**: leaves are short-TTL and expiry *is* revocation.

- **Build order (vertical slices, one PR each).** This ADR (0024) lands first and is referenced
  by the rest:
  - **#105** — `internal/pki` x509 primitives + `inforge pki` store/CLI core (`init`, `add`,
    `ls`); defines `pki.enc.yaml`.
  - **#106** — `inforge pki intermediate <name> <scope>`: per-scope intermediate minting, signed
    offline by the cold root.
  - **#107** — the `pki`/`issues`/`verifies` service schema, loader, JSON schema, and validator.
  - **#108** — deploy-time leaf minting + scoped provider write + descriptor `files:`.
  - **#109** — bootstrapper tmpfs PEM projection + `RuntimeDirectory` + the env/path contract.
  - **#110** — rotation commands + operator runbooks.

## Considered options & boundaries

- **Rejected a host-key-sealed `mesh.age` blob on disk.** It puts secret values at rest on the
  host, gives no fetch audit, and forces re-keying everything on a host-key leak — the same
  reasoning ADR-0010 used to reject baking secrets into the host. The provider path already
  exists and is lower-risk; reusing it keeps one delivery model.
- **Rejected an online CA that signs leaves on demand.** An online, sign-capable mesh key is
  precisely the asset this design removes from the runtime. Deploy-time minting from a
  CI-accessible *intermediate*, with the root kept cold, gives short-lived leaves without a
  standing online signer for the mesh.
- **Rejected a generic asset/material schema** in favour of the three concrete `pki`/`issues`/
  `verifies` fields. The concrete fields read at the service's altitude, validate precisely, and
  derive identity automatically; a generic bag would push that structure onto every author.
- **Rejected leaf CRL/OCSP.** Short TTL is the revocation mechanism; a revocation service would
  be standing infrastructure to maintain for material that expires on its own in hours.

## Consequences

- Root-key custody is split by design: the cold root keys live only behind the offline
  `rootRecipient`, so CI — and therefore any automated deploy — cannot mint a new intermediate
  or a new root on its own. Adding a region (a new scope) means an operator runs the
  intermediate-minting command with the offline key, then commits the cert.
- Rotating the **mesh root** requires a coordinated update of the daemon fleet: daemons verify
  against the mesh root but do not anchor it, so a new mesh root must be distributed to them
  before the old one is retired (a dual-root overlap window, detailed in the #110 runbooks).
- No secret *values* live at rest on a host beyond `credential.age`; leaf keys exist only in
  tmpfs for the life of a boot, and intermediate/root keys exist only in the git store and, at
  deploy, in CI memory.
- The secrets provider stays swappable: the canonical material is the git store plus the two age
  keys, and the provider holds only a per-boot projection.
- Short-TTL leaves make renewal cadence and host clock skew operationally relevant; the
  bootstrapper re-projects on every start, and leaf TTL must comfortably exceed the deploy/reboot
  interval.
