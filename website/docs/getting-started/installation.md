---
sidebar_position: 2
---

# Installation

## Download the binary

Download the latest `inforge` binary from the
[GitHub Releases](https://github.com/wardnet/inforge/releases) page.

```bash
# macOS (Apple Silicon)
curl -L https://github.com/wardnet/inforge/releases/latest/download/inforge_latest_darwin_arm64.tar.gz \
  | tar -xz inforge
chmod +x inforge
sudo mv inforge /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/wardnet/inforge/releases/latest/download/inforge_latest_darwin_amd64.tar.gz \
  | tar -xz inforge
chmod +x inforge && sudo mv inforge /usr/local/bin/

# Linux (amd64)
curl -L https://github.com/wardnet/inforge/releases/latest/download/inforge_latest_linux_amd64.tar.gz \
  | tar -xz inforge
chmod +x inforge && sudo mv inforge /usr/local/bin/
```

## Install Pulumi provider plugins

After installing `inforge`, install the provider plugins it needs:

```bash
inforge plugins install
```

This downloads `pulumi-resource-neon` and `pulumi-resource-infisical` from the matching release
and places them on `$PATH`. The Hetzner and Cloudflare providers use the standard Pulumi SDK and
require no additional installation.

## Verify

```bash
inforge --version
```

## GitHub Actions

For CI use, add the install action to your workflow:

```yaml
- uses: wardnet/inforge/.github/actions/install@v1
```

This downloads `inforge` and runs `inforge plugins install` automatically.
See [GitHub Actions: Overview](/github-actions/overview) for the full reusable workflow setup.
