# inforge

[![CI](https://github.com/wardnet/inforge/actions/workflows/ci.yml/badge.svg)](https://github.com/wardnet/inforge/actions/workflows/ci.yml)
[![Release](https://github.com/wardnet/inforge/actions/workflows/release.yml/badge.svg)](https://github.com/wardnet/inforge/actions/workflows/release.yml)
[![Docs](https://img.shields.io/badge/docs-wardnet.github.io%2Finforge-blue)](https://wardnet.github.io/inforge/)

**inforge** turns declarative YAML resource definitions into real cloud deployments using Pulumi and GitHub Actions. It enforces an opinionated multi-region, multi-provider infrastructure model with built-in runtime secret delivery for services.

📖 **Full documentation:** [wardnet.github.io/inforge](https://wardnet.github.io/inforge/)

## Quick install

```bash
# macOS (Apple Silicon) and Linux — installs to ~/.local/bin by default
curl -fsSL https://github.com/wardnet/inforge/releases/latest/download/install.sh | sh

inforge plugins install
```

Set `INFORGE_VERSION` / `INFORGE_INSTALL_DIR` to pin a version or change the destination. Once
installed, `inforge update` upgrades the CLI in place. While a newer release exists, inforge
prints a one-line notice after each command (the version check itself runs at most once a day,
and never in CI or non-interactive runs).

See [Installation](https://wardnet.github.io/inforge/getting-started/installation) for all platforms.

## Binaries

This repository builds three statically-linked, fully self-contained binaries:

| Binary | Purpose | Platforms |
|--------|---------|-----------|
| `inforge` | The CLI you invoke directly | linux amd64/arm64, darwin arm64 |
| `inforge-agent` | On-host runtime agent — the systemd ExecStart for every service; decrypts its host-key-encrypted secrets at start, drops privilege, execs the service | linux amd64/arm64 |
| `pulumi-resource-neon` | Pulumi provider plugin for Neon PostgreSQL | linux amd64/arm64 |

The provider plugins are installed automatically by `inforge plugins install`, and `inforge-agent` is downloaded onto each host by `inforge deploy` (pinned to the deploying inforge version) — you never invoke either directly.

## In GitHub Actions

inforge ships **one** action — it installs the CLI. You own your workflow and inject your providers'
secrets as env vars:

```yaml
- uses: wardnet/inforge@v1
  with:
    version: v1.6.0   # pin the CLI; default is the latest release
- run: inforge deploy prd --yes
```

See [GitHub Actions: Overview](https://wardnet.github.io/inforge/github-actions/overview) for starter
workflows and the bring-your-own-workflow model.
