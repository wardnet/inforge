---
status: accepted
date: 2026-06-12
issue: "#96"
---

# `service.host` references the compute name, not the specKey

A service's `host:` field previously referenced the compute specKey: `host: bridge-01`
(name + zero-padded instance, e.g. `bridge-01`, `bridge-02`). The specKey is an internal
expansion of the user-declared `name` and `instance_count` — it is never written in a compute
spec directly, only derived by the loader. Exposing the specKey in `host:` meant a user had
to know the expansion rule and spell out an implementation detail. It also silently allowed
referencing a multi-instance compute by one of its instances, producing an ambiguous binding.

## Decisions

- **`service.host` names the compute resource by its `name:` field** (e.g. `host: bridge`),
  not by its specKey (`bridge-01`).
- **A host compute with `instance_count > 1` is a validation error.** A service binds to a
  single VM; allowing a multi-instance target would make the binding ambiguous. Deploying a
  service to a fleet is a future concern and is not modelled here.
- **The foreign-key resolution in validate and program changes**: where the loader and program
  previously looked up the host by specKey (`ctx.computeInstances["bridge-01"]`), they now
  look up by name (`ctx.computeKind["bridge"]`) and verify `instance_count == 1`. The mapping
  seeded in the validation context uses `name` as the key.
- **The canonical specKey is still derived internally** when needed (e.g. for DNS names,
  resource identifiers): `naming.CanonicalComputeKeys` maps `bridge` → `bridge-01` when
  `instance_count == 1`. The user never writes it.
- **Website docs and validate error messages** are updated to use `host: bridge` throughout.

## Considered alternatives

**Keep `host: bridge-01` and document the expansion rule.** The current behaviour works but
leaks an internal concept. Any rename of a compute resource requires updating every service
that references it by specKey. Rejected.

**Allow `host: bridge` even for multi-instance computes, and bind to all instances.**
Under-specified: which instance gets the ingress port? Which gets the provisioned service?
Rejected until a concrete multi-instance service model exists.
