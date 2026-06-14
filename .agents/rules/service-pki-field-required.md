# Every service manifest must declare a two-tier mesh PKI membership

Every `service/*/manifest.yaml` must include a non-empty `pki:` field naming the two-tier mesh PKI
(from `pki.enc.yaml`) the service joins. The service schema enforces this as a required field, and
`inforge validate` checks that the named PKI exists, is topology `two-tier`, and has an intermediate
for every scope the service deploys under — this validation is credential-free.

## Applies to

All files matching `*/service/*/manifest.yaml` in any environment directory. Applies when creating
new services or templating service manifests.

## Example

```yaml
# regional/service/api/manifest.yaml
name: api
host: bridge
type: raw
user: api
pki: wardnet-mesh          # ← required; must be a two-tier PKI with a regional intermediate per region
ingress:
  - type: tls-termination
    listen: 443
    target: 8080
```

If the PKI lacks an intermediate for a region, `inforge validate` reports:
`pki "wardnet-mesh" has no intermediate for scope "us-east-1" — run inforge pki intermediate <env> wardnet-mesh us-east-1`
