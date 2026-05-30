# inforge

[![CI](https://github.com/wardnet/inforge/actions/workflows/ci.yml/badge.svg)](https://github.com/wardnet/inforge/actions/workflows/ci.yml)
[![Release](https://github.com/wardnet/inforge/actions/workflows/release.yml/badge.svg)](https://github.com/wardnet/inforge/actions/workflows/release.yml)

Inforge is a golang toolchain that turns declarative infrastructure definitions into real deployments using Pulumi and GitHub Actions. It enforces an opinionated structure by environment and region, enabling consistent multi-region, multi-provider infrastructure across multiple cloud providers.

## Binaries

This repository builds three statically-linked, fully self-contained binaries — no Go runtime or shared libraries are required to run them:

| Binary | Purpose |
|---|---|
| `inforge` | The CLI you invoke directly. |
| `pulumi-resource-neon` | Pulumi resource provider plugin for Neon. |
| `pulumi-resource-infisical` | Pulumi resource provider plugin for Infisical. |

> **Note:** `pulumi-resource-neon` and `pulumi-resource-infisical` are Pulumi provider plugins, not user-facing commands. They are installed automatically by `inforge plugins install`, which downloads only the providers a project needs from the matching GitHub release. You never invoke them directly.

## Installation

Each [release](https://github.com/wardnet/inforge/releases) publishes per-binary archives (`tar.gz` / `zip`) and raw, uncompressed binaries for every supported `os`/`arch`, alongside a `checksums.txt`. Download the `inforge` binary for your platform and run it — nothing else to install.
