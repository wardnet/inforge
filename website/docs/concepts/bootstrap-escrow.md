---
sidebar_position: 3
---

# Bootstrap & Escrow

inforge provides a secure mechanism for delivering secret values to VMs at first boot, without
ever placing unencrypted secrets in the repo or on the cloud provider's control plane.

## How it works

```
┌─────────────────────────────────────────────────────────────┐
│  inforge deploy                                              │
│                                                              │
│  1. Mint age key K + one-time token T                        │
│  2. Encrypt manifest secret fields with K (SOPS/age)         │
│  3. Register K under T with the escrow service               │
│  4. Write bootstrap.yaml (escrow URL + T) to VM via cloud-init│
│  5. Provision VM                                             │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼  (first boot)
┌─────────────────────────────────────────────────────────────┐
│  VM bootstrap script                                         │
│                                                              │
│  1. POST /bootstrap {token: T} → escrow returns K           │
│  2. Decrypt manifest with K                                  │
│  3. Re-encrypt manifest to host SSH key (no K needed again)  │
│  4. Delete bootstrap.yaml and K                              │
└─────────────────────────────────────────────────────────────┘
```

## The escrow service

The inforge escrow is a Cloudflare Worker (`escrow-worker/`) operated by the inforge project.

**Endpoints:**

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `PUT` | `/token` | GitHub OIDC JWT | Store key K under token T (30-minute TTL) |
| `POST` | `/bootstrap` | None | Redeem token T → K (one use only; 404 after) |

**OIDC validation:** The worker validates only the GitHub issuer
(`https://token.actions.githubusercontent.com`). Any GitHub Actions workflow can register keys —
no org allowlist is required. The `repository` claim in the OIDC token becomes the **tenant**,
preventing cross-repo key theft.

## Tenant isolation

The escrow isolates keys by tenant (`owner/repo`). A VM can only redeem tokens registered by
its own tenant. Environments within the same repo share the tenant.

## When bootstrap runs

Bootstrap only happens when the assembled manifest contains **secret values** — manifest fields
marked via the `secrets` backend. If no secret values are present, no bootstrap.yaml is written
and the VM boots without the escrow flow.

## Using the escrow in GitHub Actions

The `deploy` workflow requires `id-token: write` permission and passes the OIDC token to
inforge as the `inforge:oidc_token` stack config value. inforge creates the `HTTPEscrowClient`
and calls `PUT /token` before provisioning each VM.

```yaml
permissions:
  id-token: write
  contents: read
```

## Secret values in manifests

Secrets are sourced from the `secrets` resource using the **Source DSL**:

```yaml
source: gha:MY_SECRET          # from a GitHub Actions secret
source: ref:database/bridge.connectionUrl   # from another resource's output
```

See [Secrets resource reference](../resources/secrets) for full details.
