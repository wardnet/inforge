# Registry provider resource names must be region-scoped

`program.Run` builds one `registry.BuildRegistry` per realization scope — one per region **plus**
the region-less global slice — all sharing the same Pulumi `ctx`. Each registry lazily registers its
own cloud-provider resources (`hcloud.NewProvider`, `cf.NewProvider`). If two registries register a
provider under the same resource name, Pulumi fails the whole preview/up with
`Duplicate resource URN '…::pulumi:providers:hcloud::hcloud'`.

So every provider resource the registry creates MUST embed `r.region` in its name
(`fmt.Sprintf("hcloud-%s", r.region)`, `fmt.Sprintf("cloudflare-%s", r.region)`). `r.region` is
`"global"` for the global slice and the region name otherwise, so it is always present and unique
across the scopes a single program run builds.

## Applies to

`internal/registry/registry.go` — `hetznerProv()` and `cfProv()`. When adding any new
singleton provider resource to the registry (a `*.NewProvider(r.ctx, name, …)` call memoised via a
`sync.Once`), the `name` must be region-scoped the same way. Per-resource registrations that already
carry a unique name (the neon/infisical `ctx.RegisterResource` calls) are not affected.

## Why

A single-scope config (one region, or global-only) never exercised the collision, so a fixed name
like `"hcloud"` looked correct. The first config to realize Hetzner/Cloudflare resources in **both**
the global slice and a region — or in two regions — registers the provider twice under one URN and
fails at preview before any resource is touched. Region-scoping the name makes the provider unique
per scope while staying stable across runs of that scope.
