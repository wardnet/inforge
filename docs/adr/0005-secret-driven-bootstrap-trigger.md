# Bootstrapping is triggered by secret values in the manifest, not an explicit flag

> **Superseded by [ADR-0010](0010-runtime-secret-fetch.md).** Secrets are no longer baked into the
> manifest; services fetch their own secrets at runtime via `inforge-bootstrap`, so there is no
> manifest-secret-driven bootstrap trigger. Kept for historical context.

Manifest contributors mark individual values as secret (via `manifest.Secret`). A VM requires
bootstrapping if and only if its assembled manifest contains at least one secret value — there is no
separate "needs bootstrap" flag on compute or service. This keeps the trigger honest: it is impossible
to ship a manifest with secrets but forget to enable bootstrap, or to enable bootstrap pointlessly for
a manifest with none. The manifest generator detects secret values, emits them SOPS/age-encrypted, and
sets `bootstrapNeeded` for the caller.
