# inforge

[![CI](https://github.com/wardnet/inforge/actions/workflows/ci.yml/badge.svg)](https://github.com/wardnet/inforge/actions/workflows/ci.yml)
[![Release](https://github.com/wardnet/inforge/actions/workflows/release.yml/badge.svg)](https://github.com/wardnet/inforge/actions/workflows/release.yml)
[![Docs](https://img.shields.io/badge/docs-wardnet.github.io%2Finforge-blue)](https://wardnet.github.io/inforge/)

**inforge** turns declarative YAML resource definitions into real cloud deployments using Pulumi and GitHub Actions. It enforces an opinionated multi-region, multi-provider infrastructure model with built-in runtime secret delivery for services.

📖 **Full documentation:** [wardnet.github.io/inforge](https://wardnet.github.io/inforge/)

## Quick install

```bash
# macOS (Apple Silicon)
curl -L https://github.com/wardnet/inforge/releases/latest/download/inforge_latest_darwin_arm64.tar.gz \
  | tar -xz inforge && chmod +x inforge && sudo mv inforge /usr/local/bin/

inforge plugins install
```

See [Installation](https://wardnet.github.io/inforge/getting-started/installation) for all platforms.

## Binaries

This repository builds four statically-linked, fully self-contained binaries:

| Binary | Purpose |
|--------|---------|
| `inforge` | The CLI you invoke directly |
| `inforge-bootstrap` | Runtime secret bootstrapper — the systemd ExecStart for every service; fetches secrets at start, drops privilege, execs the service |
| `pulumi-resource-neon` | Pulumi provider plugin for Neon PostgreSQL |
| `pulumi-resource-infisical` | Pulumi provider plugin for Infisical secrets |

The provider plugins are installed automatically by `inforge plugins install`, and `inforge-bootstrap` is downloaded onto each host by `inforge deploy` (pinned to the deploying inforge version) — you never invoke either directly.

## In GitHub Actions

```yaml
- uses: wardnet/inforge@v1
```

Then call the reusable workflows for validate/preview/deploy/reconcile.
See [GitHub Actions: Overview](https://wardnet.github.io/inforge/github-actions/overview).
