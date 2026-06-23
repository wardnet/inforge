# The 127.0.0.1 loopback range [LoopbackBase, LoopbackBase+MaxMixedPorts) is reserved for ssl_preread terminators

When a public listen port is shared by tls-termination/app servers AND a `forward` (passthrough)
service — a **mixed** port — nginx cannot bind that port in both `http{}` (`ssl`) and `stream{}`. So
the public socket moves to a `stream{}` `ssl_preread` server and the `http{}` terminators move to an
internal `127.0.0.1:<loopback>` listener (`listen 127.0.0.1:<n> ssl proxy_protocol`). One loopback
port is assigned per mixed public port, ascending from `nginx.LoopbackBase` (11443), bounded by
`nginx.MaxMixedPorts` (`internal/nginx/paths.go`). See ADR-0027 and `internal/nginx/config.go`
(`Render`, `mixedPort`, `streamBlock`).

A **co-located** backend binds `127.0.0.1:<target>` on the same host nginx runs on, so a backend port
in the reserved range would collide with a terminator. Therefore a co-located service's route
`target` and its `health_probes_port` must stay **outside** `[LoopbackBase, LoopbackBase+MaxMixedPorts)`.

## Applies to

`internal/nginx/paths.go` (the exported `LoopbackBase` / `MaxMixedPorts` constants — the single source
of truth) and `internal/validate/validate.go` (`inReservedLoopbackRange`, called from `checkService`
for co-located route targets and `health_probes_port`). A cross-host backend binds on a different host
and is exempt — the collision is only possible where nginx itself runs.

## Why

The loopback terminators and a co-located backend share the host's loopback interface. Reserving a
fixed, validated range (rather than dynamically dodging used ports) keeps the rendered config
deterministic and lets validation reject the collision up front with a clear message, instead of
`nginx -t` failing mid-deploy. If you change `LoopbackBase`/`MaxMixedPorts`, the validation range
tracks them automatically — never hardcode the numbers elsewhere.
