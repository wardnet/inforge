# CertWriter must use WorkspaceID, never AdoptOrCreateWorkspace

`infisical.CertWriter` (the `inforge pki renew` write path) must resolve workspace IDs
via `client.WorkspaceID`, which errors if the workspace does not exist. Using
`AdoptOrCreateWorkspace` from the renew path would silently create workspaces that
have no per-service identity or secret policy — they would be inaccessible to the host.
Workspace lifecycle belongs exclusively to `inforge deploy`. This covers both write
paths: the per-service `/<svc>/mtls` copy (`Write`) and the per-host mesh aggregates
in the mesh workspace (`WriteMeshHost`, ADR-0033) — the mesh workspace and the
per-host identities that read it are likewise created only by deploy
(`ProvisionMeshHost`).

## Applies to

`providers/infisical/mtls.go` and any extension of `CertWriter`. The rule extends to
any future non-Pulumi write path in the infisical provider.

## Example

```go
// WRONG — creates a workspace without an identity
id, err := w.client.AdoptOrCreateWorkspace(ctx, wsName)

// RIGHT — errors loudly if deploy hasn't run yet
id, err := w.client.WorkspaceID(ctx, wsName)
```
