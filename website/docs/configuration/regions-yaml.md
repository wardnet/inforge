---
sidebar_position: 4
---

# regions.yaml

Per-environment region file at `resources/<env>/regions.yaml`. It is the **single authority** for
which regions an environment deploys into and for all provider config — credentials plus each
region's realization. The keys under `regions:` *are* the deploy set; there is no separate selector.

## Schema

```yaml
regions:                       # required — one entry per region this env deploys into
  us-east-1:                   # the abstract region name
    slug: use1                 # required — short code used in names, DNS, URNs
    dns:                       # optional — this region's DNS authority (records are created on it)
      provider: cloudflare     #   provider that owns the zone (credentials in providers.cloudflare)
      zone: abc123             #   zone id every derived record is created in
    providers:                 # required — this region's provider config
      hetzner:                 # credentials + this region's realization, in one block
        apiToken: ${HCLOUD_TOKEN}
        location: ash
        network_zone: us-east
        serverTypes: {SMALL: cx23, MEDIUM: cx33, LARGE: cx43}
        images: {ubuntu-24.04: ubuntu-24.04}
      cloudflare:
        apiToken: ${CLOUDFLARE_API_TOKEN}
      neon:
        apiKey: ${NEON_API_KEY}
      infisical:
        clientId: ${INFISICAL_CLIENT_ID}
        clientSecret: ${INFISICAL_CLIENT_SECRET}
        siteUrl: https://app.infisical.com

global:                        # optional — region-less slot (no slug)
  providers:                   # provider config for resources/<env>/global/
    neon:
      apiKey: ${NEON_API_KEY}
```

## Fields

### `regions`

A map from abstract region name (e.g. `us-east-1`) to that region's entry. The set of keys is exactly
the set of regions the environment deploys into: the single shared resource set under
`resources/<env>/` is instantiated into every region listed here. Each resource's `provider:` must be
declared in **every** region's `providers` block, since the same set deploys into all of them.

Each entry has:

- **`slug`** — the short location code the region maps to (e.g. `us-east-1` → `use1`). Slugs appear in
  display names, DNS names, and resource URNs. See
  [Environments & Regions](/concepts/environments-regions).
- **`providers`** — everything each provider needs for *this* region, one block per provider:
  credentials plus, for providers that place resources into regions (e.g. Hetzner), the region's
  **realization** directly in the block. A realization is the complete concretization of the region on
  that provider — for Hetzner: `location`, `network_zone`, `serverTypes` (size name → server-type SKU)
  and `images` (canonical image → provider image id). Realizations are fully explicit per region:
  there are no built-in defaults and no inheritance, so each region's block is the whole truth (use
  YAML anchors to de-duplicate across regions). Credentials are typically `${ENV_VAR}` references
  supplied from the environment.

Resources select which provider handles them via their own `provider:` field — configuring a provider
here does not, by itself, route any resource to it. A resource's `provider:` must be declared in its
region's `providers` block.

### `dns`

An optional per-region block naming the **single DNS authority** every derived record for that region
is created on. inforge does not have a hand-authored DNS resource: it derives the host (`<compute>.vm`),
service (`<service>.svc`) and vanity records automatically and creates them here (see [DNS](/resources/dns)).

- **`provider`** — the provider that owns the zone (e.g. `cloudflare`). Its credentials stay in the
  matching `providers` block (`providers.cloudflare.apiToken`); the authority carries only the
  selector and the zone.
- **`zone`** — the zone id records are created in (for Cloudflare, the zone id).

Different regions may declare different authorities. When a region omits `dns`, inforge creates no DNS
records for it.

### `global`

An optional, region-less slot carrying only a `providers` block (no `slug`). Resources defined under
`resources/<env>/global/` realize against it with region-less names
(`wardnet-<env>-<type>-<name>`). When omitted, the environment has no global resources.

:::caution
`regions.yaml` replaces the built-in region table entirely. There is no default fallback: an
environment with no `regions.yaml` (or an empty `regions:` map) deploys nothing.
:::

## Example

```yaml title="resources/prd/regions.yaml"
regions:
  eu-central-1:
    slug: euc1
    dns:
      provider: cloudflare
      zone: abcdef123456
    providers:
      hetzner:
        apiToken: ${HCLOUD_TOKEN}
        location: nbg1
        network_zone: eu-central
        serverTypes: {SMALL: cx23, MEDIUM: cx33, LARGE: cx43}
        images: {ubuntu-24.04: ubuntu-24.04}
      cloudflare:
        apiToken: ${CLOUDFLARE_API_TOKEN}
```
