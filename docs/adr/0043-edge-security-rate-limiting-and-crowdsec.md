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
    default_profile: standard  # applied to EVERY public server unless overridden
    profiles:
      standard:
        requests_per_second: 20
        burst: 40              # queued excess before a 429
        max_connections: 40    # concurrent conns per client IP
      strict:
        requests_per_second: 5
        burst: 10
        max_connections: 10
      api:
        requests_per_second: 100
        burst: 200
        max_connections: 200
```

**Profiles, not per-route detail** (mirrors the observability notification-profile
idiom — `Profiles map[string]Profile` + a `default_profile`, referenced by name). Limits
are named once; a route selects one with a single token. `default_profile` applies to
every public server automatically, so "all routes at 20 rps" needs zero per-route
authoring.

**Per-resource override** references a profile by name (never re-specifies limits):

```yaml
# service route (resources/<env>/regional/service/<svc>/manifest.yaml)
routes:
  - type: tls-termination
    listen: 443
    target: 8080
    rate_limit_profile: strict     # overrides the default for this route

# app (app/<name>/manifest.yaml) and gateway (gateway/<name>/manifest.yaml)
rate_limit_profile: api            # one profile per app / per gateway
```

`rate_limit_profile: none` is the reserved sentinel meaning **no limiting** on that
resource (parallels a `muted` route in notifications). `none` cannot be defined as a
real profile.

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
- **Named profiles + a default profile**, keyed by client IP (`$binary_remote_addr`).
  Modeled directly on `types.NotificationsSpec` profiles: define once, reference by
  name, one env-level default. Each *referenced* profile renders one
  `limit_req_zone`/`limit_conn_zone` in `http{}` (deduped by profile name); every server
  using it emits `limit_req zone=<p> burst=<b> nodelay;` + `limit_conn <p>_conn <n>;`.
  `limit_req_status 429;` / `limit_conn_status 429;` so rejects are `429` (CrowdSec-
  parseable), not the nginx default `503`.
- **Applies to the http edge servers** (tls-termination routes, apps, gateway). **Health
  and ACME servers are exempt** — liveness probes and cert issuance must never be
  throttled. **Forward (L4) routes** get only the profile's `max_connections` via a
  `stream{}` `limit_conn` (a stream-context zone); `requests_per_second`/`burst` do not
  apply at L4 and are ignored for a forward route (validation notes this rather than
  erroring, so a profile can serve both).
- **The private mesh nginx (`internal/meshnginx`) is untouched.** Rate limiting is an
  internet-edge concern; east-west peer traffic is already allowlist-gated.

### CrowdSec

- **The firewall bouncer (nftables), not the lua nginx bouncer.** The lua/AppSec bouncer
  needs OpenResty and would fight the stock nginx build; the firewall bouncer enforces at
  nftables from log analysis and touches no nginx config. inforge manages **no host packet
  filter today** (the firewall is the Hetzner *cloud* firewall via the provider), so the
  bouncer owns its own nft table with zero conflict — confirmed against the codebase.
- **Off-the-shelf `.deb`, checksum-verified download — mirrors ADR-0031 (otelcol).** New
  pure package `internal/crowdsec` (stdlib-only, Pulumi-free, like `internal/otelcol` and
  `internal/nginx`) renders the idempotent install shell (download the version-pinned
  `crowdsec` + `crowdsec-firewall-bouncer-nftables` `.deb`s, verify sha256, `apt-get
  install` the local files) and the on-host config. A `DefaultVersion` is pinned and
  bumped deliberately.
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

## Wiring (implementation shape)

Grounded in the existing otelcol path (`program.provisionObservability`,
`program.go:714`) and ingress realization (`program.go:1717`):

- **`internal/crowdsec`** — `paths.go` (constants: package names, service names,
  `ConfigPath`, `BouncerConfigPath`, `AcquisPath`, `DefaultVersion`, reserved-secret
  namespace/key) + `install.go` (`InstallScript`, `AcquisScript`, `BouncerScript`,
  `EnrollScript`, `WriteFileScript`). Pure, Pulumi-free.
- **`types`** — new `SecurityConfig` (with `Crowdsec` + `RateLimit` sub-blocks and
  profiles) on `EnvironmentVariables`; a `Security *bool` opt-out on `IngressSpec` and
  `GatewaySpec`; a `RateLimitProfile string` on `RouteSpec`, `AppSpec`, `GatewaySpec`.
  Accessors defaulting like `DashboardsEnabled()`.
- **`schemas`** — extend `environment.json` (the `security` block + profiles),
  `ingress.json`/`gateway.json` (`security`, `rate_limit_profile`), `service.json` (route
  `rate_limit_profile`), `app.json` (`rate_limit_profile`).
- **`loader`** — normalize/trim; default `rate_limit.default_profile`; carry profiles.
- **`validate`** — new `checkSecurity`: `default_profile` and every `rate_limit_profile`
  reference resolve to a defined profile (or `none`); profile bounds
  (`requests_per_second > 0`, `burst >= 0`, `max_connections >= 0`); reserved name `none`
  not defined; `crowdsec.enabled` with no edge host in the env is an error; console
  enrollment configured ⇒ reserved secret present.
- **`internal/nginx/config.go`** — thread the resolved rate-limit plan into `Render(...)`
  and `IngressProvider.Realize(...)`; emit the zones + per-server directives. The
  `Realize` signature change ripples to the Hetzner provider and the `types` interface
  (a mechanical, compile-checked change).
- **`program.go`** — new `provisionCrowdsec(...)` copied from `provisionObservability` but
  iterating the ingress∪gateway host set (respecting per-edge opt-out), sharing the
  memoized `gates` cloud-init map, called in the scope loop beside the observability pass.
  Rate-limit config is resolved per host and passed into `realizeIngress`.
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
  deterministic (zones deduped and sorted by profile name).
- CrowdSec keeps local state (SQLite decisions) on the edge host — surviving reloads,
  rebuilt on host replacement (bans re-learned; the community blocklist re-pulls).
- New third-party apt source on edge hosts (CrowdSec), same trade-off class as ADR-0031.

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
