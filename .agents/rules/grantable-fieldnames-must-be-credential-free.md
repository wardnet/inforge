# Grantable.FieldNames must never make network calls or read credentials

Any implementation of the `grant.Grantable` interface must keep `FieldNames(perm Permission)` free
of credentials, network I/O, and runtime state. The validator calls it on a zero-value struct
obtained from `grant.For(type)` to perform credential-free grant validation — if `FieldNames` dials
a database or reads a secret, validate breaks in CI and offline environments. The published field
names must be deterministic for a given (resource type, permission) pair, independent of any
instance's actual configuration. Only `Grant(...)` may touch credentials.

## Applies to

`internal/grant/*.go` — any file that adds a new type implementing `grant.Grantable`.

## Example

```go
// CORRECT: FieldNames is pure, keyed only on perm
func (MyResource) FieldNames(perm Permission) (values, files []string) {
    if perm == PermissionRO {
        return []string{"TOKEN"}, nil
    }
    return []string{"TOKEN", "SECRET"}, nil
}

// WRONG: FieldNames calls the API to discover which fields exist at runtime
func (MyResource) FieldNames(perm Permission) (values, files []string) {
    fields, _ := apiClient.ListFields(perm) // ← never do this; breaks the validator
    return fields, nil
}
```
