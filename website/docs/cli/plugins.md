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

The standard Pulumi provider plugins the project needs — `pulumi-resource-hcloud` (Hetzner),
`pulumi-resource-cloudflare`, and `pulumi-resource-random` (stable per-service database passwords) —
downloaded from their published releases. Self-hosted PostgreSQL (ADR-0036) ships no custom plugin.

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
