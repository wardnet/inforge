# Bootstrapping is triggered by secret values in the manifest, not an explicit flag

Manifest contributors mark individual values as secret (via `manifest.Secret`). A VM requires
bootstrapping if and only if its assembled manifest contains at least one secret value — there is no
separate "needs bootstrap" flag on compute or service. This keeps the trigger honest: it is impossible
to ship a manifest with secrets but forget to enable bootstrap, or to enable bootstrap pointlessly for
a manifest with none. The manifest generator detects secret values, emits them SOPS/age-encrypted, and
sets `bootstrapNeeded` for the caller.
