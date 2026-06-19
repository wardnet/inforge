# Use internal/app for all on-host app path derivations

`internal/app` is the single source of truth for the on-host directory
structure inforge provisions for an `app` resource: `Folder`, `CurrentPath`,
`PlaceholderDir`, and `PlaceholderIndexPath`. Never inline `/srv/wardnet/app/<name>`
or re-derive the `current` symlink path — any drift causes the provisioning
script and the nginx doc root to disagree, leaving the server block pointing
at the wrong location. This mirrors the `use-hostpaths-for-on-host-names` rule
for service workloads.

## Applies to

All packages under `cmd/`, `internal/`, and `providers/` that need the on-host
path of an app's bundle folder, current symlink, or placeholder. Currently
consumed by `program/program.go` (`appProvisionScript`, `ingressAppsByHost`) and
`internal/app/app.go` (`BuildDeployDescriptor`).

## Example

```go
// WRONG — inline derivation; can drift from the canonical form
root := "/srv/wardnet/app/" + name + "/current"

// RIGHT — import the canonical source
root := app.CurrentPath(name)   // /srv/wardnet/app/<name>/current
dir  := app.Folder(name)        // /srv/wardnet/app/<name>
```
