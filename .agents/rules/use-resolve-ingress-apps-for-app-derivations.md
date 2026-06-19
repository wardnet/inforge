# Use resolveIngressApps as the single shared app-resolution path

All three derivations that depend on which apps a host serves — the firewall
plan (public 80+443), the nginx app server blocks, and the grey-cloud DNS A
records — must be derived from `resolveIngressApps` in `program/program.go`.
Never re-resolve the `app.Ingress → ingress.Host` FK chain independently; any
divergence causes the firewall, nginx config, and DNS records to disagree on
which apps a host serves, leading to open ports with no server block or missing
DNS records with a live server block.

## Applies to

`program/program.go` and any future code in `program/` or `providers/` that
needs to enumerate apps by ingress host. The three current consumers are
`firewallPlanByHost`, `ingressAppsByHost`, and `derivedRecords`.

## Example

```go
// WRONG — duplicate FK resolution; can diverge from firewallPlanByHost
for _, a := range res.App {
    host := ingressHosts[a.Ingress]
    addRecord(naming.AppFQDN(a.Subdomain, slug, base), host)
}

// RIGHT — share the single resolved set
for _, ia := range resolveIngressApps(res, canonical) {
    addRecord(naming.AppFQDN(ia.app.Subdomain, slug, base), ia.ingHost)
}
```
