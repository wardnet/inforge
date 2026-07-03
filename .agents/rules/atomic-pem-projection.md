# Write on-host PEM files atomically (stage-to-temp, then rename)

Any code path that writes mesh PEM material (leaf cert, leaf key, trust bundle)
to the host filesystem must use a two-pass atomic projection: stage every changed
file to a sibling temp first, then rename all temps into place in a second pass.
Never write directly to the final path. A partial write — e.g. a fresh leaf cert
next to a stale or absent key — will crash-loop the service.

## Applies to

`internal/agent/project.go` (`projectFiles`) and any future write path
that places PEM files under a service's `RuntimeDir`. Also applies if projection
is extended to non-tmpfs paths.

## Example

```go
// WRONG — direct write: failure after cert but before key leaves a mismatched pair
os.WriteFile(certPath, certPEM, 0o400)
os.WriteFile(keyPath, keyPEM, 0o400)   // crash if this fails

// RIGHT — stage all, then rename all (see projectFiles in internal/agent/project.go)
tmpCert, _ := stageFile(certPath, certPEM, uid, gid)
tmpKey, _  := stageFile(keyPath, keyPEM, uid, gid)
os.Rename(tmpCert, certPath)  // both succeed or the service keeps the old set
os.Rename(tmpKey, keyPath)
```

The staging temp must live beside the destination (same directory / same tmpfs
mount) so the rename is atomic within the kernel.
