# Host VM-metrics collector

Application telemetry (ADR-0030, #134) tells us nothing about the VM itself — CPU,
memory, disk, network. A running service cannot report its host's resource usage, and
inforge owns the host, so collecting VM metrics is inforge's responsibility. We install
an off-the-shelf **OpenTelemetry Collector** on every VM, scraping host metrics and
exporting them OTLP/HTTP to the same Grafana Cloud endpoint the services use, tagged
with the **same** resource-attribute set as ADR-0030 so host metrics and app telemetry
correlate on `host.id`.

## Decisions

- **OTel Collector with the `hostmetrics` receiver → `otlphttp` exporter**, not Grafana
  Alloy or Prometheus `node_exporter`. It reuses the exact protocol, endpoint, Basic
  auth, and OTel resource-attribute model the services already use; Alloy adds a second
  config dialect and integrations we don't need, and `node_exporter` is the wrong
  protocol/model (Prom remote_write, label-based, no OTel resource attributes).
- **Off-the-shelf official `.deb` via apt, not a binary we build.** The OpenTelemetry
  Collector Contrib `.deb` ships the binary, a system user, and a systemd unit, and
  handles upgrades. A custom minimal collector via `ocb` would be more inforge-idiomatic
  (tiny static binary, checksum-verified download) but is build/maintenance overhead for
  a solved problem. inforge owns only the rendered config and the credential. Trade-off:
  `apt` from a third-party repo is less pinned/reproducible than inforge's existing
  checksum-verified raw-download invariant (`bootstrapDownloadStep`); we accept that for
  not maintaining a collector build.
- **Always-on, gated on env-level observability config.** If the env defines the OTLP
  endpoint (`variables.yaml`) and auth token (`secrets.enc.yaml`), every VM inforge
  provisions gets the collector; otherwise none is installed (it would have nowhere to
  export). No per-compute opt-in flag — host metrics are wanted uniformly, and this
  matches "we own the host."
- **Unprivileged agent; `process` scraper off.** The host signals we want (cpu, memory,
  load, disk, filesystem, network throughput) come from world-readable `/proc` and
  `/sys` — no root needed. The agent runs as the `.deb`'s unprivileged service user. Only
  the `process` scraper (per-process inventory across all users) needs root or
  `CAP_SYS_PTRACE`/`CAP_DAC_READ_SEARCH`; it stays **off** by default and is a separate
  opt-in if per-process metrics are ever wanted.
- **Credential delivered as an agent-user `0600` file, not at runtime.** The host agent
  is not a service: it has no per-service Infisical identity, no descriptor, no `files:`
  projection, and must start on boot independently. So the env-level OTLP token
  (decrypted once per deploy from `secrets.enc.yaml`) is written to the host over SSH
  (`command.remote`, the same transport that places descriptors/units/nginx config) into
  a `0600` file owned by the agent's service user, referenced by the collector's
  Basic-auth header.

## Considered options for the credential

- **`systemd-creds` encrypted-at-rest — rejected.** It keeps plaintext off disk by
  decrypting into a tmpfs cred dir at start, but it binds to a TPM2 when present, else to
  a host master key (`/var/lib/systemd/credential.secret`) that is itself on disk.
  Hetzner Cloud VMs don't expose a vTPM, so "encrypted at rest" degrades to "encrypted
  with an on-disk key" — no meaningful defense against the disk/root-exfil threat the
  tmpfs posture targets. And it costs three integration points (a unit drop-in, an
  on-host `systemd-creds encrypt` step at deploy, and a wrapper to bridge the credential
  *file* into the collector's env/config-based `basicauth`). The security gain mostly
  evaporates without a TPM, so we take the simpler `0600` file. (If TPM-backed hosts
  ever exist, revisit.)

## Consequences

- A persisted secret on the host diverges from the services' tmpfs-only,
  never-on-disk secret posture — accepted because the agent is host infrastructure that
  must boot before any inforge interaction, and the file is `0600` to the agent user only.
- New pure package `internal/otelcol` (stdlib-only, like `internal/nginx`) renders the
  collector config from the host's resource attributes + endpoint and owns the on-host
  path scheme; a `provisionObservability` pass in `program.go` installs/configures the
  agent per host, gated on env-level observability config.
