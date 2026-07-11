---
status: accepted
date: 2026-07-10
issue: "#200"
---

# Service release artifacts are keyed by CPU architecture, probed at deploy time over SSH

A production incident (wardnet-infrastructure, `tenants` service) crash-looped forever after deploy:
the CI-built binary was ARM64 (`aarch64`), the host was x86_64, and nothing in inforge's release
pipeline could ever have caught this — there was no architecture concept anywhere in
`internal/release`/`cmd/inforge`'s push/deploy path (ADR-0016). The host's own compute config was
correct; the mismatch was entirely on the service's own CI publishing the wrong build. inforge should
catch this class of error before it reaches a host, and support it going forward: an operator should be
able to swap a host's CPU architecture and rebuild, with the deploy pipeline picking the right binary
per host automatically — never silently delivering a mismatched binary again.

## Decisions

- **Service artifact keys are suffixed by architecture:** `<service>/<SHA>-<arch>.tar.gz`
  (`arch` ∈ `amd64`/`arm64`), replacing the bare `<service>/<SHA>.tar.gz` key ADR-0016 introduced for
  services. `inforge releases push` requires `--arch`; a service needing both archs is pushed twice,
  once per arch, matching the existing per-arch-CI-job precedent already used for inforge's own
  binaries. App bundles (`inforge release app`) are architecture-agnostic and unaffected — they keep
  the original unsuffixed key exactly as ADR-0016 described.
- **Architecture is detected at deploy time via SSH (`uname -m`) against each resolved target host** —
  ground truth, not a declared config field. This mirrors the existing precedent in
  `program/program.go`'s `agentDownloadStep`, which already does exactly this for inforge-agent's own
  self-install. `inforge releases deploy` probes every target host's real architecture before
  delivering, and fails loudly — naming the host, detected arch, and the exact R2 key — if any host's
  arch has no matching pushed artifact; there is no fallback to an unsuffixed key.
- **Pruning and listing still treat all arch variants of one SHA as one logical release.** A SHA isn't
  fully released until every needed arch has landed; `Prune` deletes every arch variant of a victim SHA
  together and only reports a SHA as deleted when all of its variants were removed.
- **Scope is the inforge repo only.** Consumer CI workflows (e.g. wardnet-infrastructure's
  `deploy-raw-service.yml`) that publish artifacts must push once per architecture per SHA — via a
  matrix leg per arch — before that SHA is deployable, or the deploy will hard-fail at the pre-flight
  check.

## Considered alternatives

- **Model architecture as a declared field on `ComputeSpec`/`regions.yaml`.** Rejected: it would require
  a stack re-export before `releases deploy` could use it, and host architecture is a hosting-provider
  fact discoverable at connect time — not a declared value that can drift from reality. Probing keeps
  the same "ground truth over declared state" precedent `agentDownloadStep` already established, at the
  cost of one cheap extra SSH round-trip per host.
- **Fall back to the bare unsuffixed key when no arch-specific artifact exists.** Rejected: that would
  silently mask exactly the failure mode (a not-yet-pushed arch) this ADR exists to catch loudly.

## Consequences

- Any service artifact already in R2 at the bare `<service>/<sha>.tar.gz` key is orphaned the instant
  this ships — `releases deploy` always probes an arch and always looks for the suffixed key, never the
  bare one. Every service must re-push at least one `--arch` variant for its current SHA before its
  first post-upgrade deploy succeeds. Apps are entirely unaffected.
- `releases deploy` (including its `--dry-run` path) now requires SSH connectivity to every target host,
  since probing arch is the only way to validate a fleet has every arch it needs pushed.
