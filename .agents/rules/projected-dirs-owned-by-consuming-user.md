# Every directory on a projection path must be owned by the consuming user, not just the file

When PEM material is projected onto a host, the consuming user (the service user, or `nginx` for the
mesh proxy) must own **every directory from the projection root down to the file** — not only the file
itself. Projection dirs are `0700`, so the owner's **search (`+x`)** bit is the only thing that lets the
consumer traverse to its own material. Chown the file but leave a `0700` directory owned by `root`, and
`open()` fails with `EACCES` after the privilege drop: the service crash-loops on a key that is sitting
right there, readable by nobody but root.

This bites specifically because a service's projection root comes from the unit's
`RuntimeDirectory=` + `RuntimeDirectoryMode=0700`, and the unit declares **no `User=`** (the agent runs
as root and drops privilege itself) — so systemd hands the agent a `root:root 0700` directory. Any
nested provider key (`pki/<name>/key.pem`, `mtls/leaf.crt`) then adds further `root`-owned levels via
`MkdirAll`.

## Applies to

`internal/agent/project.go` (`projectFiles` / `mkdirOwned` / `stageFile`) and any future code that
creates a directory a dropped-privilege process must read through. `internal/meshnginx` solves the same
problem in shell (`install -d -m 0700 -o nginx -g nginx`) precisely because it declares no
`RuntimeDirectory=`; do not let the two paths diverge on the invariant.

Never chown ABOVE the projection root — `/run/wardnet` is shared between services and stays root-owned.

## Example

```go
// WRONG — the file is reachable only by root: 0700 dirs owned by root deny the
// service user the search bit, so open() is EACCES after Setuid.
os.MkdirAll(filepath.Dir(dest), 0o700)   // root:root 0700
os.Chown(dest, uid, gid)                 // only the FILE is owned by the service

// RIGHT — own the whole chain from the file's dir up to (and including) the root.
mkdirOwned(runtimeDir, filepath.Dir(dest), uid, gid)
os.Chown(dest, uid, gid)
```
