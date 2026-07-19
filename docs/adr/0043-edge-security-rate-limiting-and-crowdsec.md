---
status: proposed
date: 2026-07-19
issue: "TBD"
---

# Edge security tier: rate limiting and CrowdSec

The public edge (ingress + gateway nginx) is the most-probed surface in the fleet:
scanning bots hammer `/.env`, `/wp-admin`, `/.git/config`, phpMyAdmin, and known-CVE
paths continuously. inforge terminates that traffic at a stock nginx.org build it
renders deterministically (`internal/nginx`), fronting custom Go services behind a
mesh. Today nothing throttles or bans abusive clients at that edge.

This ADR adds an **edge security tier** with two complementary, independently-toggled
capabilities, authored from one `security:` block in `variables.yaml`:

1. **Rate limiting** — nginx-native `limit_req`/`limit_conn`, rendered into the edge
   nginx.conf. Real-time L7 throttling (returns `429`) of single/small-source request
   floods. No host agent, no nginx module.
2. **CrowdSec** — a per-host agent (engine + local API) that parses the nginx access
   logs, ban decisions enforced by the **firewall bouncer** at nftables, plus the free
   crowd-sourced **community blocklist** for pre-emptive known-bad-IP bans.

The two layers feed each other: nginx throttles a flood in real time and logs the
`429`s; CrowdSec reads those logs and bans the persistent offender at nftables so it
stops reaching nginx at all.

## Scope boundary: what this does and does not stop

Being explicit, because "DoS protection" over-promises if left unqualified:

| Tier | Example | Handled by |
|---|---|---|
| L3/L4 volumetric | SYN/UDP flood, amplification | **Not the origin** — Hetzner's free network-edge DDoS protection (already in place). Saturates the uplink before nginx runs. |
| L7 single/small-source abuse | one IP at 500 req/s; brute-force; scraping | **This ADR** — nginx rate limiting + CrowdSec banning. The 95% case. |
| L7 distributed botnet flood | thousands of IPs, low rate each, real endpoint | **Partial** — the community blocklist catches known-bad IPs; the rest genuinely needs an upstream scrubber (Cloudflare). No self-hosted origin tool fully solves this, and we do not claim to. |

Choosing CrowdSec over Cloudflare's WAF is a deliberate trade: one less external
lock-in, at the cost of not covering the distributed-botnet tier. That gap is
documented, not hidden.

## Authoring

One env-level block in `variables.yaml`, with a per-edge opt-out. Both capabilities are
off unless the block enables them.

```yaml
security:
  crowdsec:
    enabled: true              # opt-in per env; installs on every public edge host
    # version: "1.6.4"         # optional pin; else inforge's DefaultVersion
    # console: true            # optional dashboard enrollment (needs reserved secret)
  rate_limit:
    enabled: true              # one blanket IP-based limit on EVERY public edge server
    requests_per_second: 20
    burst: 40                  # queued excess before a 429
    max_connections: 40        # concurrent conns per client IP
```

**One uniform limit, no per-route knobs.** IP rate limiting is a blanket *security*
measure — a floor applied identically to every public server on an edge, exactly like
CrowdSec. There is deliberately no per-route/per-app profile: a single
`requests_per_second`/`burst`/`max_connections` covers the whole edge. Per-route and
per-identity limits are a *different* concern (application logic, not a security floor)
and belong to the gateway module once authentication is at the edge — see ADR-0044; they
are enforced there (the auth service or a custom nginx dynamic module), never here.

**Per-edge opt-out** — an ingress/gateway leaves the whole security tier:

```yaml
# ingress/<name>/manifest.yaml or gateway/<name>/manifest.yaml
security: false     # this edge gets neither CrowdSec nor rate limiting
```

**Optional CrowdSec console** enrollment token is a reserved secret (never inlined),
mirroring `observability/otlp_auth`:
`inforge secret set <env> security crowdsec_enroll --reserved`. The community blocklist
needs no secret.

## Decisions

### Rate limiting

- **nginx-native `limit_req` + `limit_conn`, rendered — not a module or a sidecar.** It
  is stock-nginx.org (no lua/OpenResty), flows through the existing `internal/nginx`
  crossplane render, and preserves the deterministic-bytes property. `limit_req` is the
  leaky-bucket request-rate limiter; `limit_conn` caps concurrent connections per IP.
- **One uniform limit per edge, keyed by client IP (`$binary_remote_addr`) — no per-route
  profiles.** Rate limiting is a security floor, so the same `requests_per_second`/`burst`/
  `max_connections` is stamped on every public server of an edge, not tuned per route. The
  renderer resolves that single limit onto each `IngressRoute`/`IngressApp`/`IngressGateway`
  and emits one `limit_req_zone`/`limit_conn_zone` in `http{}` (deduped — one zone, fixed
  stem), with `limit_req zone=<z> burst=<b> nodelay;` + `limit_conn <z> <n>;` in each
  service-facing location. `limit_req_status 429;` / `limit_conn_status 429;` make a
  throttled request answer `429` (CrowdSec-parseable), not the nginx default `503`.
- **Client IP is the only key.** Accurate here — grey-cloud records and the ssl_preread
  `set_real_ip_from`/`proxy_protocol` recovery both yield the true client IP. Per-identity
  keying is out of scope for this layer entirely: it is application logic, it requires a
  *verified* identity (which the edge does not have until gateway auth lands — ADR-0032),
  and it belongs to the gateway module (ADR-0044), not the ingress security floor.
- **Applies to the service-facing http locations** (tls-termination routes, apps, gateway
  mesh routes). **Health and ACME servers are exempt** — liveness probes and cert issuance
  must never be throttled; they are rendered by separate functions that carry no limit, and
  the gateway's own edge health-probe locations are left unlimited too. **Forward (L4)
  routes** get only `max_connections` via a `stream{}` `limit_conn` (its own zone
  namespace); `requests_per_second`/`burst` do not apply at L4. A forward sharing a mixed
  ssl_preread port is not connection-limited (its socket also carries the terminators'
  traffic) — a documented v1 limitation.
- **The private mesh nginx (`internal/meshnginx`) is untouched.** Rate limiting is an
  internet-edge concern; east-west peer traffic is already allowlist-gated.

### CrowdSec

- **The firewall bouncer (nftables), not the lua nginx bouncer.** The lua/AppSec bouncer
  needs OpenResty and would fight the stock nginx build; the firewall bouncer enforces at
  nftables from log analysis and touches no nginx config. inforge manages **no host packet
  filter today** (the firewall is the Hetzner *cloud* firewall via the provider), so the
  bouncer owns its own nft table with zero conflict — confirmed against the codebase.
- **Signed apt repo (packagecloud) — mirrors the nginx.org repo pattern, not the otelcol
  `.deb` download.** CrowdSec ships `crowdsec` + `crowdsec-firewall-bouncer-nftables`
  (with dependencies) through the `packagecloud.io/crowdsec/crowdsec` apt repository, not
  as standalone checksummed `.deb`s, so the robust install adds that repo with a pinned
  `signed-by` keyring exactly as `internal/nginx/install.go` adds the nginx.org repo, then
  `apt-get install`s both packages. New pure package `internal/crowdsec` (stdlib-only,
  Pulumi-free, like `internal/otelcol`/`internal/nginx`) renders that idempotent install
  shell and the on-host config. Version pinning is optional and applies to the agent only —
  the bouncer carries its own independent version scheme (0.0.x vs the agent's 1.x), so a
  single pin cannot cover both; unpinned installs the repo's current compatible pair.
- **Community blocklist on by default; console optional.** `cscli capi register` (no
  secret) enrolls the machine to the Central API and pulls the crowd-sourced blocklist —
  the pre-emptive known-bad-IP protection that is CrowdSec's edge over fail2ban. Console
  (dashboard) enrollment is a separate opt-in gated on the reserved `crowdsec_enroll`
  secret.
- **inforge renders acquisition + bouncer config; hub content is fetched imperatively.**
  inforge writes `/etc/crowdsec/acquis.d/nginx.yaml` (pointing at `/var/log/nginx/*.log`)
  and the firewall-bouncer YAML deterministically, and leaves the package `config.yaml`
  default. Hub collections (`crowdsecurity/nginx`, `base-http-scenarios`, `http-cve`) are
  installed via `cscli collections install`, which is a **network-dependent, imperative**
  step — a deliberate deviation from the deterministic-render norm, accepted for the same
  reason ADR-0031 accepted third-party apt: not re-implementing a solved packaging
  problem. `cscli` install is idempotent (re-run is a no-op/upgrade).
- **Bouncer API key: minted like the Postgres monitor role.** The firewall bouncer
  authenticates to the local API with a key inforge generates (`random.RandomPassword`,
  the ADR-0037 monitor-password precedent), registered on-host and written into the
  bouncer config in the same pass. `cscli bouncers add` is **not idempotent** (errors if
  the bouncer exists), so the bootstrap does `cscli bouncers delete inforge-fw --force ||
  true; cscli bouncers add inforge-fw --key <key>` — safe because the key is inforge-owned
  and rewritten atomically alongside. The key routes through `ApplyT` + `safeTrigger` so
  it is encrypted in Pulumi state and never lands in the unencrypted `Triggers` array
  (enforced by the `dbrole_test.go` pattern).
- **Edges only in v1, gated on env config.** Unlike otelcol (every VM), CrowdSec installs
  only on ingress + gateway hosts — where the public HTTP traffic lands. The host set is
  the same `ingressHostUnion(routes, apps, health, gateways)` that `realizeIngress`
  computes, so it cannot drift from where nginx actually runs. Designed so a later "every
  VM" expansion (sshd brute-force coverage) is a config flip, not a rewrite.
- **No new open ports.** The local API is loopback (`127.0.0.1:8080`); the community
  blocklist pull is outbound. `types.FirewallPorts` is unchanged.
- **Observable through the existing otelcol pipeline — CrowdSec's silent failure mode
  demands it.** CrowdSec fails quietly: if it cannot read the nginx logs (rotation, path,
  permissions) it parses nothing and bans nothing while every `cscli` command still looks
  healthy. So slice 2 wires its telemetry into the pipeline inforge already runs (ADR-0031/
  0037): enable the agent's Prometheus endpoint (`127.0.0.1:6060`) and the firewall
  bouncer's loopback metrics endpoint, and add a `prometheus` receiver to the edge host's
  otelcol collector — gated on the same `observability.otlp_endpoint`, exported through the
  same `otlphttp` → Grafana Cloud, tagged with the ADR-0030 resource attributes, exactly as
  the `postgresql` receiver is added on a cluster host (ADR-0037). The load-bearing signal
  is **acquisition health** (`cs_reader_hits` / parse rate > 0 = CrowdSec is actually seeing
  traffic); the bouncer's last-pull timestamp catches enforcement drift. The deploy also
  asserts liveness at apply time — `cscli lapi status` and the bouncer appearing in `cscli
  bouncers list` — so a CrowdSec that did not come up fails the deploy rather than sitting
  dead. Dashboards and the two built-in alerts ("acquisition stalled", "bouncer not
  pulling") are a thin slice 2b once metrics flow.

## Wiring (implementation shape)

Grounded in the existing otelcol path (`program.provisionObservability`,
`program.go:714`) and ingress realization (`program.go:1717`):

- **`internal/crowdsec`** — `paths.go` (constants: package names, service names,
  `ConfigPath`, `BouncerConfigPath`, `AcquisPath`, `DefaultVersion`, reserved-secret
  namespace/key) + `install.go` (`InstallScript`, `AcquisScript`, `BouncerScript`,
  `EnrollScript`, `WriteFileScript`). Pure, Pulumi-free.
- **`types`** — new `SecurityConfig` (with `Crowdsec` + a flat `RateLimit`
  `{enabled, requests_per_second, burst, max_connections}`) on `EnvironmentVariables`; a
  `Security *bool` opt-out on `IngressSpec` and `GatewaySpec`. The resolved
  `RateLimitProfile` (a fixed-stem struct) already rides on the ingress-derived server
  structs — **no per-route spec fields, and the `IngressProvider.Realize` signature is
  unchanged.** Accessors defaulting like `DashboardsEnabled()`.
- **`schemas`** — extend `ingress.json`/`gateway.json` with the boolean `security` opt-out.
  The `security` block lives in `variables.yaml`, which is struct-decoded (like
  `observability:`), so no JSON schema changes for the block itself. No per-route schema
  changes.
- **`loader`** — normalize/trim; default the rate-limit fields.
- **`validate`** — new `checkSecurity`: rate-limit bounds (`requests_per_second > 0`,
  `burst >= 0`, `max_connections >= 0`) when enabled; `crowdsec.enabled` with no edge host
  in the env is an error; console enrollment configured ⇒ reserved secret present.
- **`internal/nginx/config.go`** — DONE (commit 1): emits the zones + per-location
  directives from the `RateLimit` field on the server structs; render is fully unit-tested,
  and a nil profile renders byte-identically to before.
- **`program.go`** — resolve the single per-edge `RateLimitProfile` from the env
  `SecurityConfig` (unless the edge opted out) and stamp it on every server the ingress
  derivations build for that host; then the new `provisionCrowdsec(...)` (copied from
  `provisionObservability`) iterating the ingress∪gateway host set, sharing the memoized
  `gates` cloud-init map, called in the scope loop beside the observability pass.
- **Docs** — public docs updated per rule `update-public-docs-with-resource-changes`;
  AGENTS.md gains a "Security (edge)" section.

## Consequences

- A persisted bouncer-API-key file on the edge host, like the otelcol/OTLP credential —
  `0600`, owned by the bouncer user. Same accepted divergence from the tmpfs-only service
  secret posture (host infrastructure that must boot independently).
- Deploy now has a network dependency on the CrowdSec hub for collection install on a host
  that has never fetched them; a hub outage fails the CrowdSec pass on a fresh edge (an
  already-provisioned host re-runs as a no-op).
- The edge nginx.conf grows `limit_req_zone`/`limit_conn_zone` blocks; render stays
  deterministic (one deduped zone per context, fixed stem).
- CrowdSec keeps local state (SQLite decisions) on the edge host — surviving reloads,
  rebuilt on host replacement (bans re-learned; the community blocklist re-pulls).
- New third-party apt source on edge hosts (CrowdSec), same trade-off class as ADR-0031.

## Future direction

- **Per-route / per-identity limits are a gateway concern, not this layer's.** This ADR is
  a blanket IP security floor. Finer limits (per route, per API key, per verified client)
  are application logic that requires a *verified* identity, and are enforced at the gateway
  module once authentication is at the edge — see ADR-0044 (gateway authentication tier),
  which owns both the `auth_request`/dynamic-module mechanism and any identity-keyed
  limiting. This ADR only notes the coupling; it intentionally ships no per-route surface.

## Considered and rejected

- **Cloudflare WAF at the edge** — strongest and lowest-ops, but adds external lock-in and
  conflicts with the grey-cloud/origin-ACME design (orange-clouding breaks origin HTTP-01
  and needs origin-IP firewalling). Explicitly declined in favor of self-hosting; may be
  revisited for the distributed-botnet tier this ADR does not cover.
- **ModSecurity + OWASP CRS / CrowdSec AppSec (true request-inspection WAF)** — needs a
  version-coupled dynamic module (ModSecurity) or lua (AppSec) against an auto-updating
  mainline nginx; high maintenance and false-positive tuning for a threat (dumb scanning)
  the banning layer already handles. Deferred until a specific app needs signature
  inspection.
- **fail2ban instead of CrowdSec** — simpler, no external dependency, but no crowd-sourced
  blocklist, which is the main pre-emptive value here.
- **A standalone `waf` asset type** — over-models: enforcement must be co-located with the
  edge host, so a separate placement FK invites a meaningless "WAF on a host with no
  ingress" case. The env-block + per-edge opt-out gives the same control with far less
  surface.
