# VM secret bootstrap uses a centralized inforge key broker plus SOPS/age re-encryption

> **Superseded by [ADR-0010](0010-runtime-secret-fetch.md).** The key broker, the one-time-token
> escrow, the SOPS/age manifest re-encryption, and the `key-broker/` Worker are all removed; services
> fetch their own secrets at runtime with a per-service identity. Kept for historical context.

Secret values in a service manifest are encrypted at provision time with SOPS/age to a key `K`.
inforge mints `K` plus a one-time token `T`, registers `K` under `T` with the **inforge key broker
service** (a Cloudflare Worker at `key-broker/`, operated by the inforge project), and writes
the broker URL + `T` into a `bootstrap.yaml`. At first boot the VM redeems `T`→`K`, decrypts the
values, and re-encrypts them to its own host SSH key (age supports ssh-ed25519), then deletes
`bootstrap.yaml`. inforge both provides and operates the key broker.

**Update (inforge 1.0):** The key broker was previously external (owned in `wardnet-infrastructure`).
It is now a first-class part of inforge (`key-broker/`). The service is open to any GitHub
Actions workflow; OIDC validates the issuer only, and the `repository` claim in the token becomes
the tenant. Self-hosted deployment support is deferred to a future release.

## Considered options & boundaries
- Rejected injecting the key via cloud provider metadata/userdata — the key broker + one-time-token model
  keeps the key off the provider's control plane and limits exposure to a single redemption.
- **Tenant = the `repository` claim** in the GitHub OIDC token (`owner/repo`): keys provisioned by
  one repo cannot be redeemed by another; environments within a repo share the tenant.
- OIDC validates issuer only (any GitHub repo) — no org allowlist. Tenant isolation at the key level
  makes cross-repo theft impossible without a matching tenant.
- SOPS/age (not raw age) so encrypted values live legibly inside the manifest YAML and can be
  re-keyed to the host identity in place.
