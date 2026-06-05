---
sidebar_position: 3
---

# variables.yaml

Per-environment variables file at `resources/<env>/variables.yaml`. Declares the regions
the environment deploys into, the provider configuration, and SSH config.

## Schema

```yaml
base_domain: example.com     # required — base domain for VM DNS names

regions:                     # required — which abstract regions this env deploys into
  - name: eu-central-1

providers:                   # provider config: credentials + region realizations
  hetzner:
    apiToken: ${HCLOUD_TOKEN}
    regions:                 # one realization per region this provider serves
      eu-central-1:
        location: nbg1
        network_zone: eu-central
        serverTypes: {SMALL: cx23, MEDIUM: cx33, LARGE: cx43}
        images: {ubuntu-24.04: ubuntu-24.04}
  cloudflare:
    apiToken: ${CLOUDFLARE_API_TOKEN}
    zoneId: abc123
  neon:
    apiKey: ${NEON_API_KEY}
  infisical:
    clientId: ${INFISICAL_CLIENT_ID}
    clientSecret: ${INFISICAL_CLIENT_SECRET}
    siteUrl: https://app.infisical.com

ssh:                         # required when using compute
  authorizedKeys: "ssh-ed25519 AAAA..."    # user access SSH public key(s)
  deployPublicKey: "ssh-ed25519 AAAA..."   # deploy user's public key
```

## Fields

### `base_domain`

The root domain for DNS names. VM hostnames are assembled as
`<subdomain>.<env>.<region-slug>.<base_domain>` (e.g. `bridge.prd.use1.wardnet.network`).

### `regions[]`

One or more **region targets** — the abstract regions this environment deploys into. Each `name`
maps to a subdirectory under `resources/<env>/<name>/`. It is a plain selector; the concrete,
provider-specific realization of each region lives under that provider's `regions` block (below).

### `providers`

Everything a provider needs, in one block per provider: credentials plus, for providers that place
resources into regions (e.g. Hetzner), a **region realization** per region. A realization is the
complete concretization of an abstract region on that provider — for Hetzner: `location`,
`network_zone`, `serverTypes` (size name → server-type SKU) and `images` (canonical image → provider
image id). Realizations are fully explicit per region: there are no built-in defaults and no
inheritance, so each region's block is the whole truth (use YAML anchors to de-duplicate across
regions). Credentials are typically written as `${ENV_VAR}` references and supplied from the
environment. See the [Hetzner provider](/providers/hetzner) page for the per-provider details.

Resources select which provider handles them via their own `provider:` field — configuring a provider
here does not, by itself, route any resource to it.

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
  - name: eu-central-1
providers:
  hetzner:
    apiToken: ${HCLOUD_TOKEN}
    regions:
      eu-central-1:
        location: nbg1
        network_zone: eu-central
        serverTypes: {SMALL: cx23, MEDIUM: cx33, LARGE: cx43}
        images: {ubuntu-24.04: ubuntu-24.04}
  cloudflare:
    apiToken: ${CLOUDFLARE_API_TOKEN}
    zoneId: abcdef123456
ssh:
  authorizedKeys: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... user@laptop"
  deployPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... deploy@ci"
```
