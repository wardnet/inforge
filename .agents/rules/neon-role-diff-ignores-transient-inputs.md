# NeonRole.Diff must ignore ownerConnectionURI and apiKey drift

`NeonRoleArgs.OwnerConnectionURI` and `ApiKey` are transient capabilities used once
at Create time to apply Postgres GRANTs. They are NOT part of the role's identity.
Neon connection URIs are not byte-stable (pooled vs direct endpoint, password reveal,
host changes), so including them in the diff would replace every per-service role
whenever the owner URI shifts — rotating all consumers' passwords unnecessarily.
`NeonRole.Diff` must only flag identity fields (`projectId`, `branchId`, `database`,
`roleName`) and `permission` as replace triggers; owner URI and API key are silently
skipped.

## Applies to

`providers/neon/cmd/pulumi-resource-neon/resources/neon_role.go` — `Diff` method,
and any future Pulumi resource in this repo that holds a "capability-only" secret
input used at provision time but irrelevant to resource identity.

## Example

```go
// CORRECT: ownerConnectionUri and apiKey are deliberately omitted from diff checks
if old.ProjectId != nw.ProjectId {
    diff["projectId"] = p.PropertyDiff{Kind: p.UpdateReplace}
}
// ownerConnectionUri and apiKey: no diff entry — drift is intentionally ignored
```
