# A daemon has no correct exit: every long-running unit is Restart=always

Every systemd unit inforge writes for a **long-running process** must carry all three of:

```
Restart=always
RestartSec=5
StartLimitIntervalSec=0
```

They are **one policy**, not three settings, and any one of them missing lets the unit end
permanently dead.

- **`Restart=always`, never `on-failure`.** `on-failure` only restarts an exit systemd
  *classifies* as a failure. A process that exits in a way systemd records as
  `Result=success` therefore goes `inactive (dead)` and **stays there indefinitely** — no
  restart, no crash-loop, no recovery. `on-failure` encodes an assumption that the process
  only ever exits on purpose. It is a meaningful distinction for a oneshot/batch unit and a
  **meaningless one for a daemon**: if the process is gone, the service is not being
  provided, whatever exit code it used on the way out.

- **`RestartSec=5`, not systemd's 100ms default.** A process that keeps dying (a bad config,
  a full disk, corrupt data) must not be relaunched ten times a second into the same wall.

- **`StartLimitIntervalSec=0`.** Without it, systemd's default limit (5 starts / 10s) ends
  the retries and parks the unit in `failed` — which is **the same permanent death
  `Restart=always` exists to prevent**, just reached faster. Recovery is the point: the unit
  must come back the moment a failing dependency does.

`ConditionPathExists=` still guards the first-boot-before-first-release gap: a failed
condition is not a *start* at all, so `Restart=` never engages, and systemd simply skips the
start silently. The guard holds unchanged under `Restart=always`.

## Applies to

`internal/service.unitTemplate` (service units) · `internal/meshnginx.UnitFile` (the mesh
proxy) · `internal/postgres.UnitFile` (the database cluster). **Any new long-running unit
template must adopt the same policy.**

## Why

This is not hypothetical. On wardnet prd, `tunneller` was sent a SIGHUP it had no handler
for (SIGHUP's default disposition is *terminate*), the process died, **systemd recorded a
clean exit**, `Restart=on-failure` never engaged — and the service stayed dead for forty
minutes until a human happened to look. Every host alert stayed green the whole time,
because the host was genuinely healthy; the only thing missing was the workload it existed
to run.

The mesh proxy and the Postgres cluster carried the identical policy and were exposed to the
identical failure — a dead mesh proxy is *every* co-located service's east-west plane, and a
dead cluster is *every* database on it. Neither had been noticed.

Do not "fix" a flapping unit by reintroducing a start limit. A unit that cannot start is a
bug to be found (that is what service-level health alerting is for); giving up on it is not
a mitigation, it is the outage.
