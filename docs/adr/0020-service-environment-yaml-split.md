# Service environment contract moves to environment.yaml; `secrets` → `environment`

A service's runtime environment variables were declared in a `secrets:` map inside the service
manifest (`service/<name>.yaml`). Two problems arose as the bootstrapper model matured: (1) the
word `secrets` conflated a general env-var contract with a secrets-management domain object —
a `LOG_LEVEL: info` literal is not a secret, yet it appeared in `secrets:`; (2) as the manifest
gained protocol, identity, and ingress fields, the env-var blob grew in the same file, making
the manifest carry two unrelated concerns.

## Decisions

- **The `secrets` field is renamed `environment`** on `ServiceSpec`. The YAML tag changes from
  `yaml:"secrets,omitempty"` to `yaml:"environment,omitempty"`. The field type is unchanged:
  `map[string]string`, with the same source DSL (`ref:`, `env:`, `vault:`, literal).
- **The env-var map is loaded from a sidecar file**, not from the manifest. Under the
  folder-based layout (ADR-0018), a service's folder is `service/<name>/`; the sidecar is
  `service/<name>/environment.yaml`. The manifest (`service/<name>/manifest.yaml`) declares
  only routing, identity, ingress, and host.
- **`environment.yaml` is a flat map** at the document root — just `KEY: value` pairs, the
  same content that previously appeared under `secrets:`. An absent sidecar means the service
  has no environment variables (equivalent to an empty map); it is not an error.
- **The `environment` field in the manifest YAML is ignored at load time** (`yaml:"-"` on the
  struct field for the manifest decoder). The loader sets `spec.Environment` explicitly from
  the sidecar after parsing the manifest.
- **All existing source DSL strings are valid unchanged** — `ref:`, `env:`, `vault:`, and
  literals work exactly as before. Only the file they live in and the struct field name
  change.
- **`inforge validate`** reports a validation error if a `vault:` entry in `environment.yaml`
  has no matching ciphertext in `secrets.enc.yaml`, unchanged from prior behaviour.
- **`inforge secret`** subcommands use the service name as a handle onto the container; this
  is unchanged.

## Considered alternatives

**Rename the field only, keep it in the manifest.** Solves the naming problem but not the
single-file conflation. Rejected.

**Keep `secrets:` in the manifest, add an optional sidecar as an override.** Two declaration
points for the same concern with a merge order to explain. Rejected.
