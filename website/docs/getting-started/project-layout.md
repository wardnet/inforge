---
sidebar_position: 4
---

# Project Layout

An inforge project has a fixed directory structure. Every path is relative to the repository root.

## Top-level files

| File | Purpose |
|------|---------|
| `inforge.yaml` | Project config: name, backend |
| `inforge.<env>.yaml` | Per-environment stack config (one file per environment) |

## resources/

All infrastructure definitions live under `resources/`.

```
resources/
└── <env>/                   # one directory per environment (e.g. prd, dev)
    ├── variables.yaml       # base_domain + SSH config for this env
    ├── regions.yaml         # which regions deploy + per-region provider config
    ├── sizes.yaml           # optional: overrides the default size table
    ├── network/             # NetworkSpec files
    ├── compute/             # ComputeSpec files
    ├── database/            # DatabaseSpec files
    ├── secrets/             # SecretsSpec files
    └── service/             # ServiceSpec files (with typed nginx ingress)
```

The resource set is defined **once** per environment and instantiated into every region listed in
`regions.yaml` — there are no per-region directories. Each cloud resource name embeds the region
slug, so instances stay unique per region (`wardnet-prd-use1-vm-bridge`,
`wardnet-prd-euc1-vm-bridge`, …). The only per-region difference is the provider realization
(location, server types, credentials), which lives in `regions.yaml`.

Each YAML file under a resource type directory contains exactly one resource spec. The filename
is used as a display hint but the identity of the resource is its `name` + `instance` fields.

## deployments/ (service repos only)

Service repos — not infra repos — contain a `deployments/` directory used by `inforge release`:

```
deployments/
├── inforge.yaml             # platform repo + service list
└── <service>.yaml           # per-environment artifact path for each service
```

`inforge release` reads these files to find the artifact and then resolves the target host,
folder, and systemd unit live from the Pulumi stack at release time.

## .pulumi/ (or state backend)

Pulumi state directory. Location depends on the `backend.type` in `inforge.yaml`:

- `file` → `.pulumi/` in the repo root (or the configured URL path)
- `git-branch` → `.pulumi-state/` (temporarily, pushed to a dedicated branch)
- `s3` / `r2` → remote object storage

## variables.yaml fields

```yaml
base_domain: example.com            # base domain for VM DNS names

ssh:
  authorizedKeys: "ssh-ed25519 ..."   # added to every VM's authorized_keys
  deployPublicKey: "ssh-ed25519 ..."  # deploy user's public key
```

See [variables.yaml](/configuration/variables-yaml) for the full reference.

## regions.yaml fields

```yaml
regions:                            # which regions this env deploys into
  eu-central-1:
    slug: euc1
    providers:                      # credentials + this region's realizations
      hetzner:
        apiToken: ${HCLOUD_TOKEN}
        location: nbg1
        network_zone: eu-central
        serverTypes: {SMALL: cx23, MEDIUM: cx33, LARGE: cx43}
        images: {ubuntu-24.04: ubuntu-24.04}
      cloudflare:
        apiToken: ${CLOUDFLARE_API_TOKEN}
    dns:
      provider: cloudflare
      zone: abc123
```

See [regions.yaml](/configuration/regions-yaml) for the full reference.
