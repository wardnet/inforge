# Bump SupportedVersion whenever Descriptor or Deployment fields change

Any addition, removal, or rename of a field in `internal/agent.Descriptor`
or `internal/agent.Deployment` is a breaking change for older agent
binaries: the YAML decoder uses `KnownFields` (strict mode), so an older reader
rejects any unrecognised field. The version check fires before field parsing, so
the failure is clean and diagnosable. Skip the bump and a fleet running mixed
agent versions can silently misread or reject descriptors.

## Applies to

`internal/agent/descriptor.go` — the `Descriptor` and `Deployment` structs
and the `SupportedVersion` constant. The rule also applies to any future struct
that is embedded in `Descriptor` and written to the on-host YAML.

## Example

```go
// WRONG — added HostID but forgot to bump SupportedVersion:
const SupportedVersion = 3  // still 3
type Deployment struct {
    ...
    HostID string `yaml:"host_id"`  // new field — older agent rejects it silently
}

// CORRECT — bump alongside the field change:
const SupportedVersion = 4  // bumped
type Deployment struct {
    ...
    HostID string `yaml:"host_id"`
}
```
