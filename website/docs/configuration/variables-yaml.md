---
sidebar_position: 3
---

# variables.yaml

Per-environment variables file at `resources/<env>/variables.yaml`. Declares the regions
the environment deploys into, the global provider defaults, and SSH config.

## Schema

```yaml
base_domain: example.com     # required — base domain for VM DNS names

regions:
  - name: us-east-1          # required — abstract region name
    providers:               # optional — region-specific provider overrides
      hetzner:
        location: ash
      cloudflare:
        zoneId: abc123

providers:                   # optional — global provider defaults
  hetzner:
    token: ""
  cloudflare:
    apiToken: ""
  neon:
    apiKey: ""
    projectId: ""
  infisical:
    clientId: ""
    clientSecret: ""
    workspaceId: ""
    environment: prd

ssh:                         # required when using compute
  authorizedKeys: "ssh-ed25519 AAAA..."    # user access SSH public key(s)
  deployPublicKey: "ssh-ed25519 AAAA..."   # deploy user's public key
```

## Fields

### `base_domain`

The root domain for DNS names. VM hostnames are assembled as
`<subdomain>.<region-slug>.<base_domain>`.

### `regions[]`

One or more region targets. Each `name` maps to a subdirectory under
`resources/<env>/<name>/`.

Per-region `providers` overrides are merged with the global defaults:
region values win for the same provider key.

### `providers`

Global provider configuration applied to all regions unless overridden.
Credentials should be empty strings — supply them via environment variables.

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
regions:
  - name: us-east-1
    providers:
      hetzner:
        location: ash
      cloudflare:
        zoneId: abcdef123456
providers:
  hetzner:
    token: ""
  cloudflare:
    apiToken: ""
ssh:
  authorizedKeys: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... user@laptop"
  deployPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... deploy@ci"
```
