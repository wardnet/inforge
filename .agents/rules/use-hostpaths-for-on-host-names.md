# Use internal/hostpaths for all on-host name and path derivations

`internal/hostpaths` is the single source of truth for the on-host names both
`inforge` and `inforge-agent` must agree on byte-for-byte: `RuntimeDir`,
`RuntimeSubdir`, and `UnitName`. Never re-derive these strings inline — any
drift causes the deploy-side and the agent binary to disagree on the
projected path.

## Applies to

All packages under `cmd/`, `internal/`, and `providers/`. Applies whenever
computing `/run/wardnet/<svc>`, `wardnet/<svc>`, or `wardnet-<svc>.service`.

## Example

```go
// WRONG — inline derivation duplicates the canonical form and can drift
dir := "/run/wardnet/" + svc
unit := "wardnet-" + svc + ".service"

// RIGHT — import the canonical source
dir  := hostpaths.RuntimeDir(svc)   // /run/wardnet/<svc>
unit := hostpaths.UnitName(svc)     // wardnet-<svc>.service
```

`internal/hostpaths` is intentionally stdlib-only so `inforge-agent` can
import it without pulling in deploy-side packages (`internal/service` →
`naming`/`types` → the Pulumi SDK). Keep it dependency-free.
