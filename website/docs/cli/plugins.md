---
sidebar_position: 5
---

# inforge plugins

Manage Pulumi provider plugin binaries.

## inforge plugins install

Download and install the provider plugins for the current release.

```bash
inforge plugins install [--version <tag>]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--version` | (current inforge version) | Plugin release tag to download |

### What it installs

- `pulumi-resource-neon` — Neon PostgreSQL provider

Binaries are downloaded from the matching GitHub release and placed in the same directory
as the `inforge` binary (or on `$PATH`). Hetzner and Cloudflare providers use the standard
Pulumi Go SDK and do not require separate installation.

### Example

```bash
# Install plugins matching the current inforge version
inforge plugins install

# Install a specific version
inforge plugins install --version v1.2.0
```

### In GitHub Actions

The [install action](/github-actions/overview#the-install-action) runs `inforge plugins install`
automatically after downloading the binary.
