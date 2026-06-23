# Toolkit

The declarative-infrastructure domain of inforge: how a project's YAML resource definitions are
loaded, validated, named, and turned into a Pulumi deployment — plus the model for hosting services
on VMs and delivering their secrets at runtime. This is the language shared by `internal/`, `program/`, and
`cmd/inforge/`.

## Language

### Structure & identity

**Environment**:
A top-level deployment target the whole config is scoped to (e.g. `prd`, `dev`). Every command takes
exactly one; inforge never acts on multiple environments at once.
_Avoid_: stage, tier.

**Region target**:
An abstract region an environment deploys into, a key under `regions:` in `regions.yaml`. The shared
resource set is defined **once** under `resources/<env>/{network,compute,…}` and instantiated into
every region (the region slug in each cloud name keeps instances unique). The set of `regions:` keys
*is* the deploy set; each entry carries the region's slug and its provider config (see **Region
realization**).
_Avoid_: "region" unqualified — see Flagged ambiguities.

**Region slug**:
The short location code an abstract region maps to (`us-east-1` → `use1`), held per region in the
**region table**. Used to build display names and the `<slug>` segment of DNS names.

**Region table**:
The per-environment `regions.yaml`: a map from abstract region to its `{slug, providers}`, plus an
optional top-level `global:` block for the global slice. It is the single authority for which regions
deploy and all provider config. The built-in slugs in `internal/regions` are naming vocabulary only;
there is no default fallback table.

**Global slice**:
The reserved, region-less scope under `resources/<env>/global/`: resources deployed **once** instead
of into every region. Not a new resource kind — the same types and schemas — with **region-less
naming** (`wardnet-<env>-<type>-<name>`, empty slug) and its own provider config in the top-level
`global:` block of `regions.yaml`. The `global:` block carries a required `placementRegion` naming
one of the abstract regions under `regions:` — used only to look up provider credentials and
realizations for global-slice resources; it does not affect resource names (see ADR-0023). A regional service's
`environment.yaml` `ref:` may target a global database/compute output via a `global/` name prefix
(`ref:database/global/<name>.<output>`) — the one allowed cross-region reference; `service.host`/
`compute.network` to global are rejected, and a global resource may reference only other global
resources.
_Avoid_: "global resource type" (global is a scope, not a kind).

**Container**:
A logical grouping label (e.g. `bridge`, `ingress`) shared by the resources that make up one unit;
the basis of URN namespaces and tags.
_Avoid_: group (old name), and do not confuse with a service delivery `type: container`.

**specKey**:
A resource instance's identity, `"<name>-<NN>"` zero-padded (e.g. `bridge-01`). Used internally as
a map key and in derived names (DNS records, display names). Not written in resource specs — foreign
references use the resource `name` directly (e.g. `service.host: bridge`, not `bridge-01`).
_Avoid_: using specKey as a user-visible foreign key in any spec field.

**Display name**:
The fully-qualified resource name `wardnet-<env>-<slug>-<type>-<name>[-<NN>]`.

### Resources

**Resource**:
One of the declarative types under a region: **Network**, **Compute**, **Database**,
**Service**. Each is a named **folder** containing a `manifest.yaml` validated against an embedded JSON
schema, plus optional sidecar files in the same folder (e.g. `cloud-init.sh` for compute,
`environment.yaml` for service — see **Resource folder** and ADR-0018). Secrets are **not** a resource
type — a service's secret/non-secret env vars live in its `environment.yaml` sidecar (ADR-0020). DNS is
**not** an authored resource (see **DNS authority**); nor is the host ingress proxy (see **Ingress**).

**Resource folder**:
The on-disk shape of a resource: `<type>/<name>/manifest.yaml`, with sidecars alongside the manifest
in the same folder. Regional resources live under `resources/<env>/regional/<type>/<name>/`;
global resources under `resources/<env>/global/<type>/<name>/`. The env-root directory holds only
environment-scoped config (`regions.yaml`, `variables.yaml`, `inforge.yaml`, `secrets.enc.yaml`,
`sizes.yaml`).
See ADR-0018 and ADR-0019.

**Compute**:
A host/runtime resource with a `kind`: `vm` (built now) or `cluster` (k8s, reserved). VM sizing is
resolved from the size table, not declared inline.
_Avoid_: server, node, instance (an instance is one expanded specKey, not the spec).

**Size table**:
The set of valid compute size *names* — cloud-agnostic vocabulary, no `cpus`/`memory` payload (a
provider maps the name to a concrete SKU; see **Region realization**). Defaults (`SMALL`, `MEDIUM`,
`LARGE`) in `internal/sizes`; a per-environment `sizes.yaml` **replaces** them wholesale.

**Service**:
A component hosted *on* a compute, referenced by its `host` field — the compute's `name` (e.g.
`host: bridge`), not its specKey. The host must have `instance_count: 1`; a multi-instance host is a
validation error. On a `kind=vm` host its delivery `type` is `raw` (a gzip of files + scripts; built
now) or `container` (pull-based; reserved). May declare an **Ingress** to be exposed for inbound
traffic. The service's runtime environment variables are declared in a sibling `environment.yaml`
sidecar (under the service's folder), not in the manifest — see **Resource folder** and ADR-0020.
_Avoid_: app, workload (acceptable informally), daemon; `host: bridge-01` (specKey form, removed).

**Ingress**:
The optional `ingress` **list** on a Service: each entry is **typed** — `type` (`tls-termination` |
`forward`), a public `listen` port, a loopback `target` port (both required, and `listen` != `target`),
and `vanity` (tls-termination only). nginx is **always** the host's sole public entry point when any
service has ingress: the service binds `127.0.0.1:<target>` and nginx fronts it. `tls-termination`
terminates ACME TLS and reverse-proxies to the target (several services may share a `listen` port,
SNI-demuxed by `server_name`); `forward` stream-forwards the raw L4 connection to the target with the
PROXY protocol (one passthrough per port; backend owns its TLS). A `forward` **may share a port** with
`tls-termination` routes: nginx `ssl_preread` routes known SNIs to the terminators and the unknown SNI
to the forward (the map default — hence one forward per port). See ADR-0027. A `tls-termination` entry owns the
service's auto-derived `<svc>.svc` SNI/cert FQDN plus any **vanity** names. There is **no** host-level
ingress resource — nginx realization is driven by ingress presence, provider taken from the host's
compute. A truly raw public port (no proxy) is a `compute.firewall.inbound` rule, not ingress. ACME owns
`:80`, so a `forward` on `:80` can't coexist with a `tls-termination` on the host. See ADR-0015.
_Avoid_: a "tls-termination resource" (removed — it is a route *type*, not a resource); authoring SNIs
by hand (they are derived — see **DNS authority**); "passthrough"/"catch-all" (the old model).

**Health probe**:
A service's optional `health_probes_port` — the **backend** port it serves health checks on. The
**Ingress** declares its own `health_probes_port` (the **public** listener, default `81`), opened only
when a referencing service declares one. nginx surfaces health as **plain HTTP** on that public port,
demuxed **strictly** by request `Host` (= the service's `<svc>.svc` FQDN / `server_name`) and
reverse-proxied to `backend:<health_probes_port>`; a wrong/absent Host is a 404 (no default server).
See ADR-0027. _Avoid_: TLS on the health port; a per-service public health port; relying on a default
server to match any Host.

**DNS authority**:
The single DNS provider + zone for one (env, region), declared in `regions.yaml` under `dns:`
(sibling of `providers:`; credentials stay in the providers block). inforge authors no DNS records —
it derives them and creates them on the authority: a host record `<compute>.vm.<env>.<slug>.<base>`,
a service record `<svc>.svc.<env>.<slug>.<base>` per ingress-bearing service, and one per vanity FQDN.
`tls-termination` routes also get an ACME cert per FQDN; a `forward` route gets a record only (the
backend owns TLS). See ADR-0014.
_Avoid_: "DNS resource" / `DnsSpec` (removed); a free-form per-host `subdomain`.

**Host ingress proxy (nginx)**:
The nginx instance inforge installs on any host that has ingress — there is **no** resource to declare
it (the old `tls-termination/` resource type was removed in ADR-0015). Realization is driven by ingress
presence: `program.realizeIngress` iterates the hosts that have ingress routes and asks the host's
compute provider to realize nginx. The Hetzner realization (`internal/nginx` renders the whole
`nginx.conf` via `nginx-go-crossplane`; `providers/hetzner` is the SSH transport) connects as the host's
`deploy_user`, installs nginx + the native ACME module from nginx.org, writes `nginx.conf`, runs
`nginx -t`, and reloads. So an ingress host **must** declare a `deploy_user` (already required for any
service). The deploy SSH **private** key is transport-only (it authenticates the connection, encrypts
nothing) and is a deploy-time secret injected via stack config / `INFORGE_DEPLOY_PRIVATE_KEY`, never
committed to `variables.yaml`.
_Avoid_: "TLS termination resource" / Caddy (removed); "load balancer"; calling it a resource.

**Deployment context (`INFORGE_DEPLOYMENT_*`)**:
The secret-free `deployment` block of a service's bootstrapper descriptor — region, region slug,
environment, base domain, `namespace` (`<env>.<slug>.<service>`), and `fqdn` (the `<svc>.svc` FQDN).
`inforge-bootstrap` injects each as an `INFORGE_DEPLOYMENT_*` env var alongside the service's secrets,
for every service (secret-bearing or not). Derived, never authored.

**Source DSL**:
A value in a service's `environment.yaml`: `ref:<type>/<name>.<output>` (a reference to another
resource's output), `vault:KEY` (a secret from the age-encrypted committed store `secrets.enc.yaml`),
`env:NAME` (an environment variable, resolved from the deploy process env via `os.Getenv`), or a bare
literal string (anything else — a verbatim non-secret value, committed in plaintext).

**Grant**:
A service's declared, permissioned access to a **Grantable** resource, materialized as a
credential/secret delivered to that service. Authored as an entry in the service manifest's `grants:`
list — distinct from the `environment.yaml` Source DSL (which only *reads* existing outputs) and from
mesh `pki:` membership (which is intrinsic identity, not a granted permission). Each entry names the
target `<type>/<name>`, a **permission** (`ro` | `rw`), and an `outputs:` block.
_Avoid_: conflating with `ref:` (a grant *creates/issues* a credential; a `ref:` only reads an
existing output) or with mesh `pki:` membership.

**Grantable**:
A resource type that can be the target of a **Grant** — currently a **Database** or a **PKI
resource**. Granting means something type-specific: for a Database, creating a scoped DB user; for a
PKI resource, delivering cert material. Each Grantable maps the universal `ro`/`rw` permission to its
own domain (DB: read-only vs read-write user; PKI: `verify` = CA cert only vs `issue` = signing key)
and publishes a permission-dependent set of **fields**.

**PKI resource**:
A grantable, root-only Certificate Authority declared as a full resource folder
(`…/pki/<name>/manifest.yaml` + a CLI-generated age-encrypted `pki.enc.yaml` sidecar holding the
root key + cert). Like every resource it has a **scope**: a regional PKI resource
(`regional/pki/<name>/`) is instantiated into every region; a global one (`global/pki/<name>/`) is
region-less. Grants obey the same cross-region boundary as the rest of the model — a regional service
may grant on its own region's PKI resource or a global one; **no cross-region access**. It is
**distinct from the mesh-auth PKI** — the special, env-root `pki.enc.yaml` store consumed via `pki:`
membership. The two never mix: a **Grant** may target only a PKI resource (root-only); the `pki:`
membership field may name only a two-tier mesh PKI. A `verify` grant delivers the root cert
(trust-only); an `issue` grant delivers the root signing key (online signer, e.g. a daemon that mints
its own short-TTL leaves).
_Avoid_: conflating the PKI resource with the mesh `pki.enc.yaml` store, or with `pki:` membership.

**Field**:
A named piece of credential material a Grantable produces for a granted permission — resource- and
permission-dependent. Each field is either a **value** field (a string, e.g. DB `USER`, `PASSWORD`,
`HOST`, `PORT`) or a **file** field (material delivered as an on-host PEM file, e.g. PKI `CERT`,
`KEY`). Fields are the placeholder vocabulary a grant's `outputs:` templates over.

**Output**:
A service-chosen environment variable a grant materializes, declared per-grant as
`outputs: { ENV_VAR: "<template>" }` where the template interpolates `{FIELD}` placeholders scoped to
that grant. A value-field template composes a string secret (`"{USER}:{PASSWORD}@{HOST}:{PORT}"`); a
file-field placeholder resolves to the projected PEM's on-host **path** (`DAEMON_CA_CERT_PATH:
"{CERT}"`). The resource publishes raw fields; the service decides its env surface — so the same
credential can be one composed URL or several discrete vars.

**Database credential access**:
A service obtains database credentials **only** through a **Grant** (a scoped per-service user) —
never by referencing a connection string via the Source DSL. A Database exposes **no** referenceable
output at all: `ref:database/<name>.…` is rejected (there is no non-credential database output today;
the `connectionUrl` was removed in slice B of #117). This keeps the database's owner/admin credential
off every consuming service. The grant mints a per-service Postgres role (`ro` = read-only, `rw` =
read/write **plus** `CREATE` on schema `public` so the service owns its own migrations) and publishes
the `{USER,PASSWORD,HOST,PORT,DBNAME}` value fields plus `{URL}` (the role's full, already-URL-encoded
connection URI — compose a DSN with `{URL}`, not a hand-assembled `{USER}:{PASSWORD}@…`).

### Providers

**Provider**:
A named cloud/service integration (`hetzner`, `cloudflare`, `neon`, `infisical`) selected per
resource by its `provider` field. The `provider` field is optional when a project-level default is
set for the resource's class in `inforge.yaml`'s `providers:` block; an explicit field always takes
precedence (see ADR-0021).
_Avoid_: confusing with a Pulumi provider object (an implementation detail inside a provider).

**Provider registry**:
The lookup that maps a provider name to the implementation satisfying a provider interface. A stub at
this phase — every lookup returns `unknown provider`.

**Provider config**:
Everything a provider needs, in one block per provider under a region's
`regions.<region>.providers.<name>` in `regions.yaml`: credentials plus that region's **realization**.
Held per region — `variables.yaml` carries no provider config.

**Region realization**:
The complete concretization of one abstract region on one provider, held directly under that region's
`providers.<name>` block in `regions.yaml` (no nested `regions` map — the enclosing entry already names
the region). For Hetzner: `location`, `network_zone`, `serverTypes` (size name → SKU) and `images`
(canonical image → provider image id). Fully explicit per region — no global defaults, no inheritance
(a realization is the whole truth for that region).
_Avoid_: "region override" (it is not an override of anything), "region config".

### Manifest & secrets

**Manifest**:
The per-instance, **secret-free** document materialised onto a VM via cloud-init, carrying only base
coordinates (version, region, namespace). Secrets are no longer baked into it — they are fetched at
runtime by `inforge-bootstrap`.

**Secret value**:
A secret a service consumes, declared in the service's `environment.yaml` sidecar via a `vault:KEY` or
`ref:` source. inforge never bakes secret values into the manifest or any other artifact; it writes them
to the secrets provider under the service's scoped path, and the service fetches them at runtime.

**Runtime secret fetch** (`inforge-bootstrap`):
Every inforge-managed service's systemd `ExecStart` is `inforge-bootstrap`, a small statically-linked
Go binary. At start it reads the service's on-host `descriptor.yaml` (secret-free: provider
coordinates + env-var → vault-key mapping), decrypts the service's `credential.age` with the host SSH
key, logs in to the provider with that machine identity, fetches the secrets, injects them as env
vars, drops privilege to the service's `user`, and execs the real binary. Secret values live only in
the child process's environment — never on disk, in the journal, or in argv. A secret-less service has
no provider and no `credential.age`, and skips the fetch entirely. See ADR-0010.

**Per-service identity**:
For each secret-bearing service, inforge mints a machine identity scoped read-only to that service's
path (`/<service>`) and writes the container's secrets under `/<service>/infra`. The standing on-host
secret is this rotatable identity credential, host-key-encrypted; a leak exposes only that one
service's path.

### Provisioning vs deployment

**Provision**:
Creating a service's host-side scaffolding during `inforge deploy` — its folder, the no-login service
user, and an inforge-managed systemd unit — written over SSH (command.remote, as the host's
`deploy_user`). The unit is enabled but **never started** at provision time (its `ExecStart` target
doesn't exist until code is released), and provisioning delivers *no* service code. A service host
must declare a `deploy_user` (validated).

**Deployment**:
The separate, repo-driven step that delivers a service's released artifact and activates it. A
service's CI **pushes** an artifact to the release store, then **deploys** a chosen SHA onto the host
via an inforge-provided reusable GitHub workflow that SSHes the artifact down and restarts the unit.
See [ADR-0016](../docs/adr/0016-r2-release-artifact-store.md).

**Deploy descriptor**:
A per-environment map of service → `{host DNS, folder, unit}`, derived purely from resolved
resources, that the deployment workflow consumes.

### Releases

**Release artifact** (or just **artifact**):
An immutable gzip of a service's built payload, keyed by commit **SHA** and stored in the release store
at `<service>/<SHA>.tar.gz`. Env-agnostic — the same artifact is deployable to any environment.
_Avoid_: build, package, image (an image is the deferred `container` delivery mode, a different thing).

**Release store**:
The R2 bucket holding release artifacts and manifests, **distinct from the Pulumi state bucket**.
Configured under `artifacts:` in `inforge.yaml` (`backend` + `keep`).

**Release manifest**:
The single mutable object per service+env, `<service>/manifest.<env>.yaml`, mapping **host** →
`{sha, deployedAt}`. The source of truth for what is live in that environment. `releases deploy` writes
it (on success); `releases list` reads it.

**Pinned SHA**:
A SHA referenced by any release manifest of a service (union across all envs). Pinning exempts an
artifact from pruning and does not count against `keep`.

**Keep**:
The number of *unpinned* (historical, rollback) artifacts a service retains. Pruning, run after each
`releases push`, deletes the oldest unpinned artifacts beyond `keep`.

## Flagged ambiguities

- **"Region"** — three distinct things; always qualify which: a *region target* (a key under
  `regions:` in `regions.yaml` — which regions an env deploys into), a *region table* entry (that
  region's `{slug, dns, providers}` in `regions.yaml`), or a *region realization* (one provider's concrete
  `{location, network_zone, serverTypes, images}` for a region, under that region's
  `providers.<name>` block).
- **"Container"** — the resource grouping label, **not** a Docker/OCI container. A service's
  `type: container` is an unrelated delivery mode. Keep the two distinct.
- **"Provider"** — the named integration chosen in a resource (`provider: hetzner`), **not** the
  Pulumi provider object an implementation constructs internally.
- **"Instance"** — one expanded `specKey` produced by a compute's `instance_count`, **not** the YAML
  spec and **not** a running VM.

## Example dialogue

> **Dev:** For the `api` service in `prd`, `host: bridge` — is that the folder name?
> **Expert:** It's the compute resource's `name` field, which matches its folder name by convention:
> `regional/compute/bridge/manifest.yaml` declares `name: bridge`. inforge expands it to the specKey
> `bridge-01` internally for DNS and display names, but users always write the bare name.
> **Dev:** And `bridge` lives in `us-east-1` — that's the region target?
> **Expert:** Right, the region target. Its slug `use1` is what shows up in the display name and the
> DNS subdomain. How `us-east-1` becomes a real Hetzner datacenter and server type — that's the region
> realization under that region's `providers.hetzner` block in `regions.yaml`, not the target itself.
> **Dev:** The service manifest has `grants: [{resource: database/bridge, permission: rw, outputs:
> {DATABASE_URL: "..."}}]`. How does the service get that secret?
> **Expert:** At deploy, inforge mints a scoped per-service Postgres role on `bridge` (not the owner
> credential), composes the `DATABASE_URL` from the role's connection fields, and writes it to the
> secrets provider under the service's path alongside a per-service identity — then drops a secret-free
> `descriptor.yaml` and a host-key-encrypted `credential.age` on the host. At service start,
> `inforge-bootstrap` decrypts the credential, fetches the secret, and execs the service with it in the
> environment. No secret value is ever baked into the manifest, and there is no `ref:database` — a
> database exposes no referenceable output. Provision set all that up — shipping the binary is a
> separate deployment.
