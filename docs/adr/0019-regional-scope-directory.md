---
status: accepted
date: 2026-06-12
issue: "#96"
---

# Non-global resources live under a `regional/` subdirectory

The environment directory (`resources/<env>/`) previously held the resource type directories
(`network/`, `compute/`, `database/`, `service/`) directly at its root, alongside config
files (`regions.yaml`, `variables.yaml`, `inforge.yaml`). This made the scope of each
entry — environment-level config vs. regionally-instantiated resource — invisible from the
directory listing. The `global/` slot already existed as a named scope; the regional set
had no symmetric peer.

## Decisions

- **Non-global resource type directories move under `regional/`**:
  ```
  resources/<env>/
    variables.yaml
    regions.yaml
    inforge.yaml
    secrets.enc.yaml          # optional git-encrypted store
    regional/
      network/
      compute/
      database/
      service/
    global/                   # unchanged
      network/ compute/ database/ service/
  ```
- **The root env directory contains only environment-scoped config files**
  (`regions.yaml`, `variables.yaml`, `inforge.yaml`, `secrets.enc.yaml`). No resource type
  directories appear at the env root.
- **The `global/` slot is unchanged** — it already carries its own scope label. The new
  `regional/` directory makes the two scopes symmetric and immediately distinguishable.
- **An absent `regional/` directory is not an error**: an environment with no regional
  resources (global-only) may omit it. An absent `global/` directory is similarly not an
  error and is unchanged from prior behaviour.
- **The loader's entry point changes**: `loadResourceSet` reads from
  `filepath.Join(envDir, "regional")` for the regional set and from
  `filepath.Join(envDir, "global")` for the global set. The validate fixtures follow the
  same layout.

## Considered alternatives

**Keep type directories at the env root, rename global to `global/`.** The existing layout
is familiar but the scope distinction requires reading the directory listing carefully. As
the global slice gains more resource types the asymmetry grows. Rejected.

**Use a `resources/<env>/<region>/` layout (per-region directories).** This was the
original approach that inforge was designed against: the single-set-per-env model exists
precisely to avoid this. Rejected.
