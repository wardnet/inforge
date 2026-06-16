# Database credentials must flow only through a grant, never through ref:database/*

`DatabaseOutputs` carries no `ConnectionURL` field and publishes no referenceable
output. Any `environment:` entry using `ref:database/<name>.<output>` is rejected by
both `inforge validate` and the deploy path (`resolveRef`). This prevents the
database owner credential from being handed to a consumer service (ADR-0025). DB
connection material reaches a service exclusively through a `grants:` entry, which
mints a scoped per-service role with only the privileges that permission level grants.

## Applies to

`internal/validate/validate.go` (`checkService`, `checkGrants`), `providers/infisical/secrets.go`
(`resolveRef`), and any code that adds a new output field to `types.DatabaseOutputs`.

## Example

```yaml
# WRONG: ref: on a database — rejected by validate and deploy
environment:
  DATABASE_URL: "ref:database/main.connectionUrl"

# CORRECT: credentials via a grant
grants:
  - resource: database/main
    permission: rw
    outputs:
      DATABASE_URL: "{URL}"
```
