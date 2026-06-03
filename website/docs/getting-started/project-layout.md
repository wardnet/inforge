---
sidebar_position: 4
---

# Project Layout

An inforge project has a fixed directory structure. Every path is relative to the repository root.

## Top-level files

| File | Purpose |
|------|---------|
| `inforge.yaml` | Project config: name, backend, key broker URL |
| `inforge.<env>.yaml` | Per-environment stack config (one file per environment) |

## resources/

All infrastructure definitions live under `resources/`.

```
resources/
└── <env>/                   # one directory per environment (e.g. prd, dev)
    ├── variables.yaml       # regions, providers, SSH config for this env
    ├── regions.yaml         # optional: overrides the default region→slug table
    ├── sizes.yaml           # optional: overrides the default size table
    └── <region>/            # one directory per region (e.g. us-east-1)
        ├── network/         # NetworkSpec files
        ├── compute/         # ComputeSpec files
        ├── dns/             # DnsSpec files
        ├── database/        # DatabaseSpec files
        ├── secrets/         # SecretsSpec files
        └── services/        # ServiceSpec files
```

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

regions:
  - name: us-east-1                 # abstract region name
    providers:
      hetzner:
        location: ash               # provider-specific region overrides
      cloudflare:
        zoneId: abc123

providers:                          # default provider config (overridden per region)
  hetzner:
    token: ""                       # set via HCLOUD_TOKEN env var
  cloudflare:
    apiToken: ""                    # set via CLOUDFLARE_API_TOKEN env var

ssh:
  authorizedKeys: "ssh-ed25519 ..."   # added to every VM's authorized_keys
  deployPublicKey: "ssh-ed25519 ..."  # deploy user's public key
```
