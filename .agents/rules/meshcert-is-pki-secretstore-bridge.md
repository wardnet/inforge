# Leaf-minting custody (decrypt intermediate key + sign leaf) lives only in internal/meshcert

`internal/pki` is deliberately free of age/secret concerns — it deals only with
certificate material and YAML-serialised store state. `internal/secretstore` holds
the AGE decryption logic. `internal/meshcert` is the one intentional seam where
these meet: it decrypts a scope's intermediate key with the CI identity and signs a
leaf from it. Keep that custody step in one place — do **not** decrypt an
intermediate key and mint a leaf anywhere else.

Note this is about the *custody logic*, not the import graph: a caller may read the
CI identity (`secretstore.IdentityFromEnv`) and the store (`pki.Load`) and pass them
to `meshcert` — `cmd/inforge` legitimately does both. What must not be duplicated is
the decrypt-intermediate-then-`GenerateLeaf` sequence.

## Applies to

All packages under `internal/` and `cmd/`. New code that needs a leaf must call
`meshcert.MintServiceLeaf` / `meshcert.IntermediateSigner` + `meshcert.MintLeaf`,
never re-implement `secretstore.Decrypt(intermediate.Key, …)` → `pki.GenerateLeaf`.

## Example

```go
// WRONG — re-implements the custody step outside the bridge
keyPEM, _ := secretstore.Decrypt(inter.Key, ciIdentity)
signer, _ := pki.ParsePrivateKey(string(keyPEM))
leaf, _, _ := pki.GenerateLeaf(interCert, signer, spiffeID, svc)

// RIGHT — go through the bridge (caller still loads store + CI identity)
leafPEM, keyPEM, err := meshcert.MintServiceLeaf(store, pkiName, scope, ciIdentity, domain, env, svc)
```
