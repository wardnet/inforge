# VM secret bootstrap uses an external multi-tenant escrow plus SOPS/age re-encryption

Secret values in a service manifest are encrypted at provision time with SOPS/age to a key `K`.
inforge mints `K` plus a one-time token `T`, registers `K` under `T` with an **external** escrow
service (owned in `wardnet-infrastructure`, made multi-tenant separately), and writes the escrow URL +
`T` + tenant into a `bootstrap.yaml`. At first boot the VM redeems `T`→`K`, decrypts the values, and
re-encrypts them to its own host SSH key (age supports ssh-ed25519), then deletes `bootstrap.yaml`.
inforge **integrates with, but does not implement,** the escrow.

## Considered options & boundaries
- Rejected injecting the key via cloud provider metadata/userdata — the escrow + one-time-token model
  keeps the key off the provider's control plane and limits exposure to a single redemption.
- **Tenant = the repo** (`owner/repo`): keys provisioned by one repo cannot be redeemed by another;
  environments within a repo share the tenant.
- SOPS/age (not raw age) so encrypted values live legibly inside the manifest YAML and can be
  re-keyed to the host identity in place.
