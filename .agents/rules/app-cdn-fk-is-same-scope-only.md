# An app's cdn: foreign key must resolve to a cdn in the same scope — never global/

An app declared in the regional slice must reference a cdn in that same region.
A `global/` prefix is explicitly rejected by `checkApp` in `internal/validate/validate.go`.
An app served from a global cdn is declared in the global slice itself, not referenced
cross-scope; this mirrors the `service.host` rule (a service's host must be a compute in
the same scope).

## Applies to

`regional/app/<name>/manifest.yaml` and `global/app/<name>/manifest.yaml` — any `cdn:` field.
Also applies when implementing future realization code that resolves the FK at deploy time.

## Example

Bad — a regional app referencing a global cdn:
```yaml
# regional/app/dashboard/manifest.yaml
cdn: global/main   # rejected: "references a global cdn; declare in the global slice"
```

Good — same-scope reference:
```yaml
# regional/app/dashboard/manifest.yaml
cdn: main          # resolves to regional/cdn/main/manifest.yaml in this region
```

Good — global app with global cdn:
```yaml
# global/app/dashboard/manifest.yaml
cdn: main          # resolves to global/cdn/main/manifest.yaml
```
