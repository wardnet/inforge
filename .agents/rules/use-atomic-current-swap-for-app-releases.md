# Use internal/app for the atomic `current` swap — never hand-roll it

An `app`'s served document root is the `current` symlink under its folder
(`internal/app.CurrentPath`). Two paths repoint it: provisioning seeds it at the
placeholder (Slice C), and release swaps it to a delivered `<sha>` bundle
(Slice D). Both MUST go through the single contract in `internal/app` so they
agree byte-for-byte on the symlink shape:

- `app.BundleDir(name, sha)` — the per-SHA directory a bundle is delivered into
  (`<Folder>/<sha>`). Each SHA gets its own sibling directory; that is what makes
  a rollback a symlink repoint instead of a re-fetch.
- `app.SwapCurrentScript(name, sha)` — the atomic swap: a **relative** symlink
  (target is the bare `<sha>`) staged under a temp name and renamed over `current`
  with `mv -T`, so one `rename(2)` flips the document root and nginx never
  observes a missing or half-written `current`.
- `app.GCReleasesScript(name)` — prunes old bundles beyond `app.KeepReleases`,
  excluding the placeholder and whatever `current` resolves to.

Never inline `ln -s`/`ln -sfn`/`mv` against `current`, never use an absolute
symlink target, and never use a non-atomic two-step (`rm current && ln -s …`) — a
non-atomic swap serves a dangling or half-delivered root to live requests, and an
absolute target breaks if the app folder moves. The provisioning seed
(`program.appProvisionScript`) intentionally only *creates* `current` when it is
absent (it must not clobber a released bundle on re-provision); the release path
*replaces* it via `SwapCurrentScript`.

## Applies to

Any code in `cmd/`, `internal/`, or `program/` that points or repoints an app's
`current` symlink or names a per-SHA bundle directory. Current consumers:
`program/program.go` (`appProvisionScript`) and `cmd/inforge/release.go`
(`appReleaseScript`, `appRollbackScript`). Mirrors `use-internal-app-for-app-paths`.

## Example

```go
// WRONG — non-atomic, absolute target, inlined path
script := "sudo rm -f /srv/wardnet/app/my/current && " +
    "sudo ln -s /srv/wardnet/app/my/abc123 /srv/wardnet/app/my/current"

// RIGHT — the single atomic contract
script := app.SwapCurrentScript("my", "abc123")
```
