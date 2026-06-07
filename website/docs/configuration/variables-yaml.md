---
sidebar_position: 3
---

# variables.yaml

Per-environment variables file at `resources/<env>/variables.yaml`. It holds the base domain and SSH
config for the environment. Which regions deploy and all provider config (credentials + per-region
realizations) live in [`regions.yaml`](/configuration/regions-yaml).

## Schema

```yaml
base_domain: example.com     # required — base domain for VM DNS names

ssh:                         # required when using compute
  authorizedKeys: "ssh-ed25519 AAAA..."    # user access SSH public key(s)
  deployPublicKey: "ssh-ed25519 AAAA..."   # deploy user's public key
```

## Fields

### `base_domain`

The root domain for DNS names. VM hostnames are assembled as
`<subdomain>.<env>.<region-slug>.<base_domain>` (e.g. `bridge.prd.use1.wardnet.network`).

### `ssh`

SSH keys placed on every provisioned VM:

- `authorizedKeys` — added to the VM's authorized_keys for human (admin) access.
- `deployPublicKey` — the SSH public key installed for the deploy user. When a compute
  resource declares a [`deploy_user`](/resources/compute#deploy-user), inforge provisions
  that account at VM-init time and installs this key into its `authorized_keys`. The
  username itself is set per-compute in `deploy_user.name`; the key material lives here
  so that rotating it only requires updating `variables.yaml` and re-running `inforge deploy`.

## Example

```yaml title="resources/prd/variables.yaml"
base_domain: example.com
ssh:
  authorizedKeys: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... user@laptop"
  deployPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... deploy@ci"
```
</content>
