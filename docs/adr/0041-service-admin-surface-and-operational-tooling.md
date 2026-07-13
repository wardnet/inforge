---
status: accepted
date: 2026-07-13
---

# Service admin surface and operational tooling

A transient mesh `403` went undiagnosed for hours. The caller's reconcile loop logged the
failure every tick and logged *nothing* on success, so "still broken" and "recovered
fifteen seconds ago" were indistinguishable in Grafana. The obvious reach — turn on debug
logging — was unavailable: `RUST_LOG` is read once at process start, so changing it means
editing `environment.yaml`, deploying, and **restarting the very process under
investigation**, which destroys the state you are trying to observe and clears the
transient you are trying to catch.

So we lack two things: a way to see what a running service is doing, and a way to ask a
running service a question without restarting it. This ADR defines both, and — as
importantly — records what we deliberately refused to build.

## The governing principle

> **The in-process contract holds only what nothing else can answer.**

We already have root SSH on every host. Anything reachable that way does not earn HTTP
surface on a live service. Applying this ruthlessly is what kept the contract to three
endpoints; every capability below was proposed, tested against the principle, and most of
them died.

## Decisions

- **Two planes, one noun.** Operator-facing commands all live under `inforge service`
  (which already hosts `restart`). Underneath, they split by substrate: the **SSH plane**
  (systemd + shell on a host inforge already reaches as a sudo-capable `deploy` user) and
  the **in-process plane** (HTTP the service itself must implement). That split is
  load-bearing for *us* — it decides what needs Rust — but it is an implementation fact, so
  it never surfaces as a CLI noun. An operator restarting a service should not have to know
  which mechanism carries the verb. A second noun, `inforge host`, covers machine-level
  operations; it is **pure SSH, with no resident daemon** (see Rejected).

- **inforge owns and evolves the contract; services implement it partially.** inforge
  already defines an opinionated runtime contract with the services it deploys — the
  strict-decoded `agent.Descriptor` (v7), the reserved `INFORGE_*` env namespace, the
  `MTLS_*_PATH` reservations, the mesh's `X-Mesh-Target`/`X-Service-Identity` header
  semantics. An admin contract is the same species. Pretending inforge is application-
  agnostic here would be fiction, and a "generic transport" alternative would still only
  ever be pointed at one admin API — just with no validation, no version check, and no
  typed errors. Paths and payloads are single-sourced in `internal/adminapi`, the way
  `meshpaths`/`hostpaths` single-source names shared between the CLI and the agent.

- **Capabilities and version are separate axes.** `GET /admin` returns
  `{contract_version, capabilities, service_version, uptime, instance_id}`. **Capabilities**
  are *presence* — which verbs this service actually implements, so a service that has not
  built one is reported as unsupported rather than 404ing. **Version** is *shape* — when a
  payload changes, both sides know. Capabilities alone cannot express a changed request
  body; a version alone forces lockstep implementation of everything. Together they mean
  the Go toolchain and the Rust services never need a coordinated release.

- **The admin listener gets its own port, configured per service.** `admin_port` on the
  service manifest, joining the int-keyed TCP collision space in `checkService` beside route
  targets, `health_probes_port` and `exposed_ports` (and clear of the nginx loopback range,
  the mesh egress range, and `postgres.ClusterPort`). It cannot be a reserved constant: a
  host runs several service processes, which would collide on a fixed port — unlike
  `meshpaths.MTLSPort` or `nginx.LoopbackBase`, which belong to a single per-host nginx.
  `DeployTarget` carries `AdminPort` so the CLI resolves it from the same stack output it
  resolves hosts from, and targeting can never disagree with deploy.

- **NOT the health port.** inforge supports a cross-host backend, and for one the firewall
  opens the service's `health_probes_port` to the **private network CIDR**. Sharing that
  port would mean the admin API silently goes on the wire the first time somebody rehomes a
  service to a different host from its ingress — an exposure produced by a change nobody
  would connect to admin security. And nginx would not save us: the private-CIDR rule
  exposes the backend port *directly*, so the ingress's exact-match/404-catch-all guard is
  not in that path at all. A separate port keeps the admin surface out of every firewall and
  nginx derivation entirely.

- **inforge injects the bind address; the manifest only names the port.** `buildEnv` emits
  `INFORGE_ADMIN_ADDR = 127.0.0.1:<admin_port>` (reserved name, rejected as an
  `environment:` key by validate). Were the address authored like `HEALTH_LISTEN_ADDR`, then
  `0.0.0.0` would be one careless edit away and loopback-only would be a convention we merely
  trust people to follow — which is exactly what the separate port was meant to escape. The
  manifest says *which port*; inforge says *which interface*. There is no syntax in which an
  operator can express "expose admin on the network". This is the same division that already
  governs `mesh.port` (authored) and `INFORGE_MESH_URL` (injected).

- **The runtime log-level override is always time-boxed.** `PUT /admin/log-filter` takes an
  optional `ttl_seconds` (default 30m, ceiling 6h); on expiry the process reverts to the
  filter declared in `environment.yaml`. There is no "until further notice". A permanent
  runtime override is **config drift that git cannot see** — a host left on `trace` for
  three weeks, invisible in the repo, discovered via the ingest bill. That is precisely the
  failure declarative infrastructure exists to prevent, and we will not reintroduce it
  through the back door of an admin API. So `RUST_LOG` in `environment.yaml` and the runtime
  toggle are **complements, not competitors**: the first is the declared steady state (in
  git, reviewed, survives restart, never expires); the second is a time-boxed investigation
  (memory only, never persisted, dies on restart, always expires). "I want debug on
  permanently" correctly answers to *"then commit it."*

- **Transition-aware logging in the consuming services' background loops.** A shared
  `wardnet_common::loop_health::LoopHealth` reporter: first failure WARN, repeats suppressed
  to DEBUG, **recovery INFO with the failure count and run duration**, steady-state success
  silent. This is the actual fix for the motivating incident and needs no contract, no port,
  and no inforge change — the 403 burst becomes two lines that say what happened instead of
  3,031 that do not. Note it would *not* have been fixed by a debug toggle: the success path
  logged nothing **at any level**, so `RUST_LOG=debug` would have produced the same silence.

  It is applied to the **tick-level** operations — the eight an interval loop retries
  forever: `ddns.provisioner.fetch`, `ddns.reaper.fetch`, `tunneller.abort_reaper.list_owned`,
  `tunneller.ttl_reaper.reap`, `tenants.catalog_sync`, `tenants.tombstone_sweep`,
  `tenants.subscription_reaper`, `tenants.subscription_reconcile`. Those are the sites with
  the pathology: an unkeyed operation retried forever, whose sustained failure means the loop
  is doing *nothing at all* while emitting one identical line per tick. Two operations that
  merely share a tick (tenants' reaper and reconcile) get **separate** reporters — one can be
  failing while the other is healthy, and one fault must not mask the other's or reset its run.

  The remaining five sites are **per-item** failures (`network_id = …`), deliberately left
  one-line-per-occurrence: they are bounded by the queue's real contents, each is individually
  actionable, and collapsing them would need a per-item keyed tracker whose entries leak when
  an item leaves the queue. A persistently-failing *item* is a poison-item problem — a
  different fault with a different fix.

  **Report the outcome of the attempt, once.** Reporting from inside a pagination or retry
  loop makes a partially-failing attempt alternate success and failure, which flaps a WARN
  and a *fabricated* recovery every tick — worse than no reporting at all, because the
  recovery line asserts something untrue. The first implementation did exactly this and had
  to be fixed: the reporting call must sit at the outcome of the whole operation, which in
  ddns meant splitting `*_tick` (reports, once, as a single match) from `*_drain` (fetches).
  Relatedly, a failing run is keyed on **which** error, not merely on "am I failing" — a fault
  that changes identity mid-run (the 403 you fixed becoming a connection refusal) must warn
  again rather than be suppressed as more of the old one.

  **Census the pattern, not the phrase.** The first pass grepped for the string
  `"retry next tick"` — ddns and tunneller's wording — and concluded the pattern spanned nine
  sites. Tenants words its loops differently, so its four operations were missed entirely and
  the grep was then reasoned about as if it were a complete survey. It spans **thirteen**. The
  correct census reviews every `loop {}` and asks which are periodic-retry loops; the ones
  legitimately left alone are accept loops, stream/parse loops, a bounded resolve-then-insert
  race retry, and a broadcast-recv loop whose `Lagged` gap is an event rather than a sustained
  fault run.

- **A mesh ping answered by the mesh proxy, not the service.** A reserved
  `location = /_inforge/ping` in `meshnginx.ingressServer`, **inside the allow guard**,
  returning 200 (or 403 to a disallowed caller). Derived and injected into every callee's
  render, never authored — like gateway routes — and validate rejects any authored
  `public_paths`/`internal_paths` entry under the reserved `/_inforge/` prefix. A single call
  through the caller's loopback egress then exercises the whole chain: egress config, routing
  table, network reachability, the TLS handshake (both leaves *and* the trust bundle), and
  the allow-list — every layer that failed in the incident — with no side effects. Answering
  it in the **proxy** rather than the app is deliberate: the failure was in nginx's allow-map
  and the app never saw the request, so a proxy-answered ping isolates *"is the mesh path
  working"* from *"is the app healthy"*. Conflating them is what makes a 502 ambiguous. App
  liveness stays in the health tier.

- **Artifact integrity is a three-way checksum comparison, not a self-reported build SHA.**
  A service deploy untars over a flat, static path (`/srv/wardnet/<svc>/<binary>` — no `<sha>`
  dir, no `current` symlink, unlike apps), so today *nothing on the host knows which SHA is
  running*. inforge records what it delivered in an on-host release manifest; `service
  instances` then compares **delivered** (the manifest) vs **on disk** (`sha256sum` of the
  binary) vs **executing** (`sha256sum /proc/<MainPID>/exe`, which resolves to the inode the
  process is actually running). `delivered ≠ on disk` means the files were modified after
  deployment. `on disk ≠ executing` means a new binary is staged but the process never
  restarted onto it — running yesterday's code while every dashboard claims otherwise, a
  silent failure invisible to any manifest-based check. This needs no Rust, no `build.rs`,
  and no contract field, and it detects strictly more than a baked-in git SHA would.
  Deploy-through-inforge remains a **policy**: the `deploy` user has sudo by necessity, so
  this is tamper-**evident**, not tamper-**proof**. It catches accident, drift, and staleness
  — not an attacker who already owns the box.

- **`--host`/`--region` are filters; `--instance` is a precondition.** `service instances`
  lists the live processes (host, region, `service.instance.id`, uptime, version, deployed
  SHA, integrity). Because the instance id is regenerated on every restart it is not merely
  an address but a **version token for the running process**, so `--instance <id>` means "act
  only if it is still this process". An operator who lists, thinks for thirty seconds, and
  then fires a command at a service that crash-looped in between gets `instance abc123 is gone
  (now def456) — the service restarted`, which is a safe failure *and* the diagnostic they
  needed. Compare-and-swap, for one equality check.

## The contract

```
GET    /admin              -> {contract_version, capabilities, service_version, uptime, instance_id}
GET    /admin/log-filter   -> {filter, default, expires_at}
PUT    /admin/log-filter   -> {filter, ttl_seconds?}   400 on an EnvFilter that will not parse
DELETE /admin/log-filter   -> revert to the declared default
        ... the same three for /admin/trace-filter
```

That is all of it. `log-filter` and `trace-filter` survive because **only the process can
swap its own `EnvFilter`** — nothing else, at any privilege level, can do it without a
restart. `info` is folded into discovery because that endpoint must exist anyway for
capabilities, and `service.instance.id` (random per start) answers *"is this the same
process I looked at ten minutes ago?"*, which nothing else can.

## Rejected

- **`drain`.** There are **no `upstream {}` blocks anywhere** — neither the north-south nginx
  nor the mesh proxy — so every `proxy_pass` points straight at a single resolved address.
  nginx OSS cannot do active health checks, and with no upstream block there is no passive
  failover either. **Nothing in the data path reads `/readyz`** (its only consumer is the
  external status prober). A `drain` verb would flip the readiness flag, report success, and
  100% of traffic would keep flowing — not a missing feature but a *lie*, and worse than
  absent, because a restart would be sequenced behind it in the false belief that the blip
  was avoided. Structurally, every service is single-instance per host: **there is nothing to
  drain to.** Restart causes a brief blip and that is honest. Drain returns as a *prerequisite
  of multi-node rolling deploys* (upstream blocks, `proxy_next_upstream`, node-by-node
  deployment), which is its own ADR.

- **`config` (redacted).** Proposed to answer "did the secret actually land". But as the
  sudo-capable `deploy` user you can already read `/proc/<pid>/environ` and see the **actual**
  environment the process runs with. A redacted view (presence + hash) is *strictly less
  information* than SSH already gives, while being a fresh HTTP surface holding
  secret-adjacent data. The threat model does not even close: anyone who can reach the
  loopback admin port has already sudo'd on the box.

- **`restart` as a contract endpoint.** A process restarting itself over its own HTTP endpoint
  must exit and trust systemd's `Restart=` to catch it — no ordering control, no failure
  reporting. `inforge service restart` (SSH + `systemctl restart` + `is-active` verification)
  is strictly better and, crucially, **works on a wedged process that cannot answer HTTP** —
  which is exactly when you most want to restart it.

- **`mesh-check` as a contract endpoint.** Needs no service code at all: the mesh egress
  listener is plain-HTTP on loopback and *nginx* attaches the client certificate, so anything
  on the host can already make a fully-authenticated mesh call as that service. It is an SSH-
  plane verb, costing nothing in Rust and nothing in contract surface.

- **A resident inforge host daemon.** The architecture has consistently *removed* long-lived
  inforge machinery from hosts — ADR-0035 killed the pull timers for push-over-SSH, the agent
  `exec()`s into the service rather than supervising it, the mesh `ExecStartPre` is a one-shot
  local decrypt. A daemon reverses all of it: a listener, its own authentication (loopback-only
  makes it useless remotely, so mTLS, so a leaf, so mesh membership…), its own update path, and
  a remote-code-execution surface on every VM — to buy what hardened, keyed, sudo-capable SSH
  already provides. And it would not even help the motivating case: only the service process
  can swap its own `EnvFilter`, so a daemon could not do the reload either.

## Consequences

- Services need `admin_port` on the manifest to have any admin surface at all; without it,
  inforge reports "no admin surface" and the SSH-plane commands still work. Adoption is
  therefore incremental and never blocking.
- The reserved `/_inforge/` mesh path prefix and the `INFORGE_ADMIN_ADDR` env name join the
  existing reservation set that `validate` enforces.
- `internal/adminapi` becomes the single source of truth for admin paths and payloads —
  producer and consumer derive from it, so they cannot drift.
- Log volume in steady state *drops*: transition-aware logging replaces per-tick failure spam,
  and the runtime toggle's mandatory TTL prevents a host being stranded in debug.
