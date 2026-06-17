# Use resolveComputeHost for every compute host: foreign-key check

Any resource type whose `host:` field is a foreign key into the scope's compute
set must resolve and validate that FK through `resolveComputeHost(host, noun, ctx)`
in `internal/validate/validate.go` — never hand-roll the lookup. The helper owns
the canonical error messages, the bare-name vs. specKey distinction, and the
single-instance vm guard; duplicating its logic produces divergent validation
behaviour across resource types.

## Applies to

`internal/validate/validate.go` — any new `check<Resource>` function for a type
that declares a `host:` field referencing a compute resource (e.g. future
resource types in slice B+). Currently used by `checkService` and `checkIngress`.

## Example

Bad — hand-rolled host lookup:

```go
if _, ok := ctx.computeNames[s.Host]; !ok {
    errs = append(errs, fmt.Sprintf("host: %q not found", s.Host))
}
```

Good — delegate to the shared helper:

```go
canonical, hostErrs := resolveComputeHost(s.Host, "a widget", ctx)
errs = append(errs, hostErrs...)
```
