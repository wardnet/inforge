---
status: accepted
date: 2026-06-12
issue: "#96"
---

# Resource definitions are named folders containing manifest.yaml

Each YAML file under a resource type directory previously held exactly one resource spec and
was named after the resource (`bridge.yaml`, `ingress-01.yaml`). Sidecar files — cloud-init
scripts and the environment contract — were placed in the same type directory, distinguished
by a name prefix (`bridge.cloud-init.sh`). This coupled the sidecar name to the resource name
as a fragile prefix convention and cluttered the directory with mixed file kinds.

## Decisions

- **Each resource is a named folder**, not a file. The folder sits under its type directory
  (`compute/bridge/`, `service/api/`); the resource spec lives inside as `manifest.yaml`.
- **Sidecar files live alongside `manifest.yaml`** in the same folder, with their own names
  unambiguous within that context:
  - Compute sidecar: `cloud-init.sh` (not `bridge.cloud-init.sh`).
  - Service sidecar: `environment.yaml` (see ADR-0020).
- **A folder with no `manifest.yaml` is a loader error**, not silently skipped. A resource
  boundary is explicit: a folder exists iff a manifest exists.
- **Stray files** at the type directory level (alongside folders, not inside them) are ignored.
  The loader walks entries that are directories; plain files at that level are inert.
- **The folder name is cosmetic**; the resource identity comes from the `name:` field inside
  `manifest.yaml`. Folder and spec name should agree by convention (the loader does not enforce
  equality), so `compute/bridge/manifest.yaml` should declare `name: bridge`.
- **The `cloud_init` field in a compute spec is relative to the compute folder**, not to the
  compute type directory. `cloud_init: cloud-init.sh` resolves to
  `compute/<name>/cloud-init.sh`. Absolute paths are also accepted.

## Considered alternatives

**Keep flat files with sidecar-by-prefix.** `bridge.cloud-init.sh` beside `bridge.yaml` is
simple for small resource sets, but the name-prefix coupling is fragile (rename the resource,
rename the sidecar), and as the resource count grows the type directory becomes hard to scan.
Rejected.

**Separate sidecar directory.** `compute/sidecars/bridge/cloud-init.sh`. Keeps type dirs
flat but splits a resource's files across two directories. Rejected in favour of co-location.
