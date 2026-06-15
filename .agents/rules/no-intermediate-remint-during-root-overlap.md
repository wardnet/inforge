# Never re-mint an intermediate while a root overlap is active

When a dual-root rotation is in progress (`PKI.PreviousRoots` is non-empty), re-minting
an intermediate with a fresh key would produce a cert signed by the new root whose chain
the old root cannot validate. Leaves signed by that intermediate would be unverifiable by
peers whose trust bundle was built before `--finalize` — the old root's trust anchoring
is no longer valid for a new-root-signed chain. Prevent this by checking for an active
overlap before any intermediate re-mint and returning an error that directs the operator
to finalize the root rotation first.

## Applies to

`cmd/inforge/pki.go` `runPkiRotateIntermediate` and `runPkiRecoverIntermediate` (both
call `reissueIntermediate`). Any future code path that calls `mintScopeIntermediate` on
an existing scope must apply the same guard.

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
