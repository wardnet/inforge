# Cold-root custody must gate both the begin and finalize steps of a root rotation

The offline root identity (`INFORGE_PKI_ROOT_KEY`) is required not only to start a
dual-root overlap (minting a new root and re-signing intermediates) but also to finalize
it (dropping the retained old root from `PreviousRoots`). Finalizing without the cold
identity check would allow CI — or a stale repo checkout — to retire the old root before
all consumers have migrated their trust bundles, breaking verification for any leaf that
was issued by the old root and has not yet been renewed.

## Applies to

`cmd/inforge/pki.go` `runPkiRotateRoot` and any future root-lifecycle operation that
promotes, retires, or replaces a cold root in the PKI store. The pattern: every path that
writes to `PKI.Root` or shrinks `PKI.PreviousRoots` must first call
`pki.RootIdentityFromEnv()` and verify it can decrypt the active root key.

## Example

```go
// WRONG — finalizes without verifying the caller holds the cold identity
p.PreviousRoots = nil
store.Set(name, p)
store.Save(path)

// RIGHT — gate on the offline identity before any root-retiring write
rootIdentity, err := pki.RootIdentityFromEnv()
if err != nil { return err }
if _, err := secretstore.Decrypt(p.Root.Key, rootIdentity); err != nil {
    return fmt.Errorf("decrypt current root key: %w", err)
}
p.PreviousRoots = nil
store.Set(name, p)
store.Save(path)
```
