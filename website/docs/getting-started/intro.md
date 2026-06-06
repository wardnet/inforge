---
slug: /
sidebar_position: 1
---

# Introduction

**inforge** is a Go toolchain that turns declarative YAML resource definitions into real cloud
deployments using Pulumi and GitHub Actions. It enforces an opinionated, multi-region,
multi-provider infrastructure model with built-in runtime secret delivery for services.

## What inforge does

1. **Validates** your resource definitions against JSON schemas
2. **Previews** what Pulumi would create, update, or destroy
3. **Deploys** your infrastructure via the Pulumi Automation API
4. **Delivers secrets** to services — writing each service's provider coordinates and a
   host-key-encrypted machine-identity credential to the host, so the service fetches its own secrets
   at runtime (no secret value is ever baked into an artifact)
5. **Provides reusable GitHub Actions** so consumer repos get preview/deploy/reconcile
   workflows with a single `uses:` line

## Supported providers

| Provider | Resources |
|----------|-----------|
| Hetzner Cloud | Network, Compute |
| Cloudflare | DNS |
| Neon | Database (PostgreSQL) |
| Infisical | Secrets |

## Quick links

- [Installation](/getting-started/installation)
- [Quick Start](/getting-started/quick-start)
- [Project Layout](/getting-started/project-layout)
- [Resource Reference](/resources/network)
