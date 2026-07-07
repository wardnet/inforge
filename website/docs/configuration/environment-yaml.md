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

  # Provider credentials (typically set via env vars, not committed here)
  hcloud:token: ""                    # set via HCLOUD_TOKEN
  cloudflare:apiToken: ""             # set via CLOUDFLARE_API_TOKEN
```

## inforge-specific config keys

| Key | Description |
|-----|-------------|
| `environment` | Environment name. Used by the Pulumi program to find resource files. |

The deploy SSH private key (used to SSH each host at `pulumi up` to realize host-level resources and
write each service's descriptor/credential) is supplied at deploy time via the
`INFORGE_DEPLOY_PRIVATE_KEY` environment variable, not committed here.

## Example

```yaml title="inforge.prd.yaml"
config:
  environment: prd
  hcloud:token: ""
  cloudflare:apiToken: ""
```

:::caution
Never commit real tokens or API keys. Leave credential fields empty (`""`) and supply
them via environment variables or GitHub Actions secrets at run time.
:::
