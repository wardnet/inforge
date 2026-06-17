# An app's ingress: foreign key must resolve to an ingress in the same scope — never global/

An app declared in the regional slice must reference an ingress in that same region.
A `global/` prefix is explicitly rejected by `checkApp` in `internal/validate/validate.go`.
An app served from a global ingress is declared in the global slice itself, not referenced
cross-scope; this mirrors the `service.host` rule (a service's host must be a compute in
the same scope) and the `ingress.host` rule (an ingress's host must be a compute in the
same scope).

## Applies to

`regional/app/<name>/manifest.yaml` and `global/app/<name>/manifest.yaml` — any `ingress:` field.
Also applies when implementing future realization code that resolves the FK at deploy time.

## Example

Bad — a regional app referencing a global ingress:
```yaml
# regional/app/dashboard/manifest.yaml
ingress: global/web   # rejected: "references a global ingress; declare in the global slice"
```

Good — same-scope reference:
```yaml
# regional/app/dashboard/manifest.yaml
ingress: web          # resolves to regional/ingress/web/manifest.yaml in this region
```

Good — global app with global ingress:
```yaml
# global/app/dashboard/manifest.yaml
ingress: web          # resolves to global/ingress/web/manifest.yaml
```
