---
sidebar_position: 2
sidebar_label: "inforge.<env>.yaml"
---

# inforge.&lt;env&gt;.yaml

Per-environment stack config. One file per environment, named `inforge.<env>.yaml`
(e.g. `inforge.prd.yaml`).

These values are passed into the Pulumi program as stack config. Values that are empty
strings are expected to come from environment variables at run time.

## Schema

```yaml
config:
  environment: prd                    # the environment name (passed to the Pulumi program)
  inforge:broker_url: https://...     # key broker service URL
  inforge:oidc_token: ""              # set at deploy time from GitHub OIDC
  inforge:tenant: ""                  # defaults to GITHUB_REPOSITORY env var

  # Provider credentials (typically set via env vars, not committed here)
  hcloud:token: ""                    # set via HCLOUD_TOKEN
  cloudflare:apiToken: ""             # set via CLOUDFLARE_API_TOKEN
  neon:apiKey: ""                     # set via NEON_API_KEY
  infisical:clientId: ""              # set via INFISICAL_CLIENT_ID
  infisical:clientSecret: ""          # set via INFISICAL_CLIENT_SECRET
```

## inforge-specific config keys

| Key | Description |
|-----|-------------|
| `environment` | Environment name. Used by the Pulumi program to find resource files. |
| `inforge:broker_url` | URL of the key broker service. |
| `inforge:oidc_token` | GitHub Actions OIDC JWT. Required when manifests have secrets. Set at deploy time — never commit a real token. |
| `inforge:tenant` | Key broker tenant (`owner/repo`). Defaults to `GITHUB_REPOSITORY` env var. |

## Example

```yaml title="inforge.prd.yaml"
config:
  environment: prd
  inforge:broker_url: https://key-broker.inforge.wardnet.network
  inforge:oidc_token: ""
  hcloud:token: ""
  cloudflare:apiToken: ""
```

:::caution
Never commit real tokens or API keys. Leave credential fields empty (`""`) and supply
them via environment variables or GitHub Actions secrets at run time.
:::
