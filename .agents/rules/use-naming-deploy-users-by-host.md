# Use naming.DeployUsersByHost for the deploy-user map — never re-derive it

`naming.DeployUsersByHost` is the single shared derivation of the
`specKey → deploy_user` map for all compute hosts. Never re-implement this
mapping inline (looping over `res.Compute`, checking `c.DeployUser != nil`,
expanding instance counts). Duplicating it produced the bugs that the slice C
consolidation fixed: the old private `deployUsersByHost` in `program/program.go`
emitted an empty string for hosts with no `deploy_user`, while the deploy
descriptors in `internal/service` and `internal/app` used the same expansion
logic — each independently — so any future change to the expansion rule would
have to be applied to all three.

## Applies to

Any code in `cmd/`, `internal/`, or `program/` that needs the deploy user for
a compute host. Currently used by `program/program.go` (`realizeIngress`,
`provisionApps`, `provisionServices`) and both deploy descriptor builders
(`internal/app.BuildDeployDescriptor`, `internal/service.BuildDeployDescriptor`).

## Example

```go
// WRONG — hand-rolled expansion; must be kept in sync manually
byHost := map[string]string{}
for _, c := range res.Compute {
    if c.DeployUser != nil {
        for i := 1; i <= c.InstanceCount; i++ {
            byHost[naming.SpecKey(c.Name, i)] = c.DeployUser.Name
        }
    }
}

// RIGHT — delegate to the canonical derivation
byHost := naming.DeployUsersByHost(res.Compute)
```
