# Never mint or re-mint an intermediate while a root overlap is active

When a dual-root rotation is in progress (`PKI.PreviousRoots` is non-empty), the cold root
is already the NEW root. Signing any intermediate then — re-minting an existing scope with
a fresh key, OR minting a brand-new scope — produces a cert that chains only to the new
root, which a consumer still anchored on the old root cannot validate. (Re-mint additionally
orphans the old-key leaves the overlap exists to keep verifying.) Every intermediate mint
path must therefore refuse while an overlap is active and direct the operator to finalize
the root rotation first.

## Applies to

`cmd/inforge/pki.go`: `runPkiIntermediate` (first-mint of a new scope),
`runPkiRotateIntermediate` and `runPkiRecoverIntermediate` (re-mint, both via
`reissueIntermediate`). Any code path that calls `mintScopeIntermediate` — first-mint or
re-mint — must apply the same `len(p.PreviousRoots) > 0` guard.

## Example

```go
// WRONG — re-mints mid-overlap; old-root peers cannot verify the new intermediate chain
mat, err := mintScopeIntermediate(store, p, name, scope)

// RIGHT — guard before re-minting
if len(p.PreviousRoots) > 0 {
    return fmt.Errorf("PKI %q is in a root overlap — finalize it first", name)
}
mat, err := mintScopeIntermediate(store, p, name, scope)
```
