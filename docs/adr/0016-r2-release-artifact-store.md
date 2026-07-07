---
status: accepted
date: 2026-06-09
issue: "#82"
---

# Releases are SHA-keyed artifacts in R2, fronted by a per-env manifest

Until now `inforge release` packaged a service's local artifact directory into a gzip and SSH-pushed it
straight onto the host (ADR-0007). There was no record of what was deployed and no way to roll back to a
previously shipped build. We decided to make a released build a **first-class, immutable artifact stored
in R2**, keyed by commit SHA, and to split release into a producer and a consumer step under a new
`inforge releases` command group.

## Decisions

- **Artifacts are env-agnostic, keyed by SHA:** `<service>/<SHA>.tar.gz` in a dedicated R2 bucket
  (distinct from the Pulumi-state bucket; the two-bucket separation is validated at config load). One
  built artifact can be deployed to any environment — build once, promote the same bytes.
- **A per-env manifest is the source of truth for what is live:** `<service>/manifest.<env>.yaml`, a
  host-keyed map `host → {sha, deployedAt}`. It is the only mutable object in the store.
- **`inforge releases push`** (producer) builds + uploads the artifact, then prunes. **`inforge releases
  deploy`** (consumer) downloads a SHA, delivers it over SSH + restarts the unit (the ADR-0007 transport,
  unchanged), and on success updates the env manifest. **`inforge releases list`** reads the manifest.
  `--sha` is required on `deploy`; the local-directory delivery path is removed.
- **Pruning never deletes a live build.** `push` keeps the newest `keep` (config in `inforge.yaml`
  `artifacts:`) unpinned artifacts by R2 `LastModified` and deletes older ones, but a SHA referenced by
  **any** `manifest.<env>.yaml` for that service is *pinned* and exempt — pinned builds do not count
  toward `keep`. Because artifacts are env-agnostic, the pin set is the union across all env manifests, so
  a prune triggered by a qa push can never evict a SHA still live in prd.
- **Manifest updates use an `If-Match` compare-and-swap.** `deploy` reads the manifest with its ETag,
  sets its host's entry, and PUTs with `If-Match: <etag>`; a `412` means a concurrent writer won, so it
  refetches and retries (bounded). A GitHub Actions concurrency group serialises the normal path; the CAS
  is what keeps a direct-from-laptop `releases deploy` safe.

## Considered alternatives

- **Keep env in the artifact path (`<service>/<env>/<SHA>.tar.gz`).** Rejected: it forces re-uploading
  identical bytes per environment and forecloses promoting the exact tested artifact from qa to prd.
- **A `deployed.tar.gz` copy instead of a manifest.** Rejected: it cannot express more than one live
  build, so it breaks the moment a service runs on multiple hosts/instances with different SHAs. The
  host-keyed manifest models that directly.
- **Per-host pin objects (`<service>/deployed/<host>.yaml`) to dodge manifest write races.** Rejected
  once R2's S3 API was confirmed to support `If-Match` on `PutObject`: the single manifest the operator
  reasons about is worth keeping, and the CAS removes the race without splitting the file.

## Consequences

- The R2 layout and `manifest.<env>.yaml` schema become a compatibility surface the first time a deploy
  writes a manifest; changing either later is a migration.
- A service repo must configure the `artifacts:` block to release at all (the local-dir path is gone).
- An artifact is unpinned between `push` and `deploy`; a burst of ≥ `keep` pushes before a deploy can
  evict it. The mitigation is that `deploy` fails loudly when the SHA is absent from R2.
- Promotion of one artifact across prefixes/envs is now a cheap R2 `CopyObject` away (a future
  `inforge releases promote`) — nothing in this layout precludes it.
