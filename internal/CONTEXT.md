# Toolkit

The declarative-infrastructure domain of inforge: how a project's YAML resource definitions are
loaded, validated, named, and turned into a Pulumi deployment — plus the model for hosting services
on VMs and bootstrapping their secrets. This is the language shared by `internal/`, `program/`, and
`cmd/inforge/`.

## Language

### Structure & identity

**Environment**:
A top-level deployment target the whole config is scoped to (e.g. `prd`, `dev`). Every command takes
exactly one; inforge never acts on multiple environments at once.
_Avoid_: stage, tier.

**Region target**:
An abstract region an environment deploys into, declared in `variables.yaml` `regions[]` with its
per-region provider overrides. Resources live under `resources/<env>/<region>/`.
_Avoid_: "region" unqualified — see Flagged ambiguities.

**Region slug**:
The short location code an abstract region maps to (`us-east-1` → `use1`), held in the **region
table**. Used to build display names and DNS subdomains.

**Region table**:
The abstract-region → slug map. Built-in defaults live in `internal/regions`; a per-environment
`regions.yaml`, when present, **replaces** them wholesale.

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
**Secrets**, **Service**. Each is one YAML file validated against an embedded JSON schema.

**Compute**:
A host/runtime resource with a `kind`: `vm` (built now) or `cluster` (k8s, reserved). VM sizing is
resolved from the size table, not declared inline.
_Avoid_: server, node, instance (an instance is one expanded specKey, not the spec).

**Size table**:
The compute size name → `{cpus, memory}` map. Defaults (`SMALL 2/4`, `MEDIUM 4/8`, `LARGE 8/16`) in
`internal/sizes`; a per-environment `sizes.yaml` **replaces** them wholesale.

**Service**:
A component hosted *on* a compute (its `host` foreign key). On a `kind=vm` host its delivery `type`
is `raw` (a gzip of files + scripts; built now) or `container` (pull-based; reserved).
_Avoid_: app, workload (acceptable informally), daemon.

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

### Manifest & bootstrap

**Manifest**:
The per-service document materialised onto a VM via cloud-init, describing how to run that service.
Assembled by manifest contributors.

**Manifest contributor**:
A component that adds fields to a service's manifest (e.g. the secrets backend). It may mark
individual values as a **secret value**.

**Secret value**:
A manifest field flagged sensitive (wrapped via `manifest.Secret`). Its presence — not any separate
flag — is what makes a VM require bootstrapping; secret fields are stored SOPS/age-encrypted.

**Bootstrap**:
The one-time first-boot step a VM performs when its manifest has secret values: fetch the key from
the key broker, decrypt, and re-encrypt the values to the host's SSH key.

**Key Broker**:
The multi-tenant key broker inforge owns and operates as a Cloudflare Worker (`key-broker/`).
inforge mints key `K` + one-time token `T`, registers `K` under `T` via the key broker, and the VM
redeems `T`→`K` at first boot. Open to any GitHub Actions workflow; the `repository` claim in
the GitHub OIDC token becomes the tenant, enforcing cross-repo isolation. No consumer needs to
host their own key broker.

**Tenant**:
The key broker isolation boundary = the **repo** (`owner/repo`). Keys provisioned by one repo cannot be
redeemed by another; environments within a repo share the tenant.

### Provisioning vs deployment

**Provision**:
Creating a service's host-side scaffolding — its folder, metadata, and an inforge-managed systemd
unit. Does *not* deliver service code.

**Deployment**:
The separate, repo-driven step that delivers a service's payload (a gzip) and activates it, via an
inforge-provided reusable GitHub workflow that SSHes the artifact onto the host.

**Deploy descriptor**:
A per-environment map of service → `{host DNS, folder, unit}`, derived purely from resolved
resources, that the deployment workflow consumes.

## Flagged ambiguities

- **"Region"** — means either a *region target* (a deployment region + its provider overrides) or a
  *region slug / region table* entry (the abstract→slug mapping). Always qualify which.
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
> DNS subdomain. The target also carries the Hetzner overrides for that region.
> **Dev:** The secrets file has `source: ref:database/bridge.connectionUrl`. The VM needs that secret
> at boot?
> **Expert:** The secrets backend contributes it to `bridge`'s manifest as a secret value. Because a
> secret value is present, the VM bootstraps: it redeems its token with the key broker for the key,
> decrypts, and re-encrypts to its own SSH key. Provision set all that up — actually shipping the
> service binary is a separate deployment.
