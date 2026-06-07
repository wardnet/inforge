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
An abstract region an environment deploys into, a key under `regions:` in `regions.yaml`. Resources
live under `resources/<env>/<region>/`. The set of `regions:` keys *is* the deploy set; each entry
carries the region's slug and its provider config (see **Region realization**).
_Avoid_: "region" unqualified — see Flagged ambiguities.

**Region slug**:
The short location code an abstract region maps to (`us-east-1` → `use1`), held per region in the
**region table**. Used to build display names and DNS subdomains.

**Region table**:
The per-environment `regions.yaml`: a map from abstract region to its `{slug, providers}`. It is the
single authority for which regions deploy and all provider config. The built-in slugs in
`internal/regions` are naming vocabulary only; there is no default fallback table.

**Container**:
A logical grouping label (e.g. `bridge`, `ingress`) shared by the resources that make up one unit;
the basis of URN namespaces and tags.
_Avoid_: group (old name), and do not confuse with a service delivery `type: container`.

**specKey**:
A resource instance's identity, `"<name>-<NN>"` zero-padded (e.g. `bridge-01`). The value other
resources use as a foreign key.

**Display name**:
The fully-qualified resource name `wardnet-<env>-<resourceType>-<slug>-<specKey>`.

### Resources

**Resource**:
One of the declarative types under a region: **Network**, **Compute**, **DNS**, **Database**,
**Secrets**, **Service**, **TLS termination**. Each is one YAML file validated against an embedded
JSON schema.

**Compute**:
A host/runtime resource with a `kind`: `vm` (built now) or `cluster` (k8s, reserved). VM sizing is
resolved from the size table, not declared inline.
_Avoid_: server, node, instance (an instance is one expanded specKey, not the spec).

**Size table**:
The set of valid compute size *names* — cloud-agnostic vocabulary, no `cpus`/`memory` payload (a
provider maps the name to a concrete SKU; see **Region realization**). Defaults (`SMALL`, `MEDIUM`,
`LARGE`) in `internal/sizes`; a per-environment `sizes.yaml` **replaces** them wholesale.

**Service**:
A component hosted *on* a compute (its `host` foreign key). On a `kind=vm` host its delivery `type`
is `raw` (a gzip of files + scripts; built now) or `container` (pull-based; reserved). May declare an
**Ingress** to be exposed for inbound traffic.
_Avoid_: app, workload (acceptable informally), daemon.

**Ingress**:
The optional `ingress` block on a Service (`hostname`, `tls`, `port`) declaring that it is exposed for
inbound traffic. Ingress is fed to the host's **TLS termination** resource, which writes one vhost per
service that terminates TLS and reverse-proxies to the service's local port. A service that declares
ingress *must* have a TLS termination resource on its host; a service that wants raw inbound traffic
instead opens the port itself on the host firewall and declares no ingress.
_Avoid_: confusing the per-service `ingress` field with the host-level terminator (the resource).

**TLS termination**:
A host-level resource (its own YAML type, `tls-termination/`) declaring a terminator the compute
provider realizes on a host: it terminates inbound TLS and reverse-proxies to the services running
there. On Hetzner this is realized by Caddy (ACME / Let's Encrypt); another provider could realize the
same resource with a managed load balancer + ACM. Targets a host via its `compute` foreign key. The
Hetzner realization (`internal/caddy` for rendering, `providers/hetzner` for transport) runs over SSH
via `command.remote`: it connects as the host's `deploy_user`, installs Caddy + tooling, writes a base
Caddyfile that imports per-service vhosts from `conf.d/`, and reloads. So a terminator's host **must**
declare a `deploy_user` (validated). `internal/caddy` is a Hetzner-internal detail, not a top-level
concept. The deploy SSH **private** key is transport-only (it authenticates the connection, encrypts
nothing) and is a deploy-time secret injected via stack config / `INFORGE_DEPLOY_PRIVATE_KEY`, never
committed to `variables.yaml`.
_Avoid_: "ingress" (that is the per-service field that feeds this), "load balancer", "proxy" (unqualified).

**Source DSL**:
A Secrets `source` value: `ref:<type>/<name>.<output>` (a reference to another resource's output) or
`gha:<NAME>` (a GitHub Actions secret). Anything else is invalid.

### Providers

**Provider**:
A named cloud/service integration (`hetzner`, `cloudflare`, `neon`, `infisical`) selected per
resource by its `provider` field.
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
A secret a service consumes, declared in a Secrets resource by container. inforge never bakes secret
values into the manifest or any other artifact; it writes them to the secrets provider under the
service's scoped path, and the service fetches them at runtime.

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
The separate, repo-driven step that delivers a service's payload (a gzip) and activates it, via an
inforge-provided reusable GitHub workflow that SSHes the artifact onto the host.

**Deploy descriptor**:
A per-environment map of service → `{host DNS, folder, unit}`, derived purely from resolved
resources, that the deployment workflow consumes.

## Flagged ambiguities

- **"Region"** — three distinct things; always qualify which: a *region target* (a key under
  `regions:` in `regions.yaml` — which regions an env deploys into), a *region table* entry (that
  region's `{slug, providers}` in `regions.yaml`), or a *region realization* (one provider's concrete
  `{location, network_zone, serverTypes, images}` for a region, under that region's
  `providers.<name>` block).
- **"Container"** — the resource grouping label, **not** a Docker/OCI container. A service's
  `type: container` is an unrelated delivery mode. Keep the two distinct.
- **"Provider"** — the named integration chosen in a resource (`provider: hetzner`), **not** the
  Pulumi provider object an implementation constructs internally.
- **"Instance"** — one expanded `specKey` produced by a compute's `instance_count`, **not** the YAML
  spec and **not** a running VM.

## Example dialogue

> **Dev:** For `bridge` in `prd`, the DNS file points at `compute: bridge-01`. Is that the file name?
> **Expert:** It's the specKey — `bridge` with instance `01`. If `bridge`'s `instance_count` were 2,
> you'd have `bridge-01` and `bridge-02`, and a DNS resource targets one of them.
> **Dev:** And `bridge` lives in `us-east-1` — that's the region target?
> **Expert:** Right, the region target. Its slug `use1` is what shows up in the display name and the
> DNS subdomain. How `us-east-1` becomes a real Hetzner datacenter and server type — that's the region
> realization under that region's `providers.hetzner` block in `regions.yaml`, not the target itself.
> **Dev:** The secrets file has `source: ref:database/bridge.connectionUrl`. How does the service get
> that secret?
> **Expert:** inforge writes it to the secrets provider under the service's path and mints a per-service
> identity, then drops a secret-free `descriptor.yaml` and a host-key-encrypted `credential.age` on the
> host. At service start, `inforge-bootstrap` decrypts the credential, fetches the secret, and execs the
> service with it in the environment. No secret value is ever baked into the manifest. Provision set all
> that up — actually shipping the service binary is a separate deployment.
