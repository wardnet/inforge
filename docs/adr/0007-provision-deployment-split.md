---
status: accepted
date: 2026-05-30
issue: "#15"
---

# Provisioning and deployment are separate lifecycles

inforge **provisions** a service's host-side scaffolding — its folder, metadata, and an
inforge-managed systemd unit (so inforge owns restart/update) — but does **not** deliver service code.
**Deployment** is a separate, repo-driven step: a service's own CI calls an inforge-provided reusable
GitHub workflow that SSHes a gzip of files+scripts onto the host (using the deploy key), extracts it
into the provisioned folder, and restarts the unit. Splitting the two keeps infrastructure changes and
code releases on independent cadences and lets each service repo own its release pipeline while inforge
owns the runtime contract. Only `raw` (SSH-push) delivery is built now; `container` (pull-based) is
deferred.
