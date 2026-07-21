---
status: proposed
date: 2026-07-19
issue: "TBD"
---

# Gateway authentication tier

Today the north-south daemon gateway (ADR-0032) routes and forwards: it TLS-terminates,
matches the request path against the derived public-path globs (ADR-0034), and hands the
request to the local mesh egress with `X-Mesh-Target`, forwarding the daemon's
`Authorization` header **untouched** for the owning service to validate. Authentication is
entirely a service concern.

This ADR moves **authentication** to the gateway while keeping **authorization** at the
service. The gateway becomes the first gate that produces a *verified* identity; the
service still enforces what that identity may do. This is a companion to ADR-0043 (edge
security tier) — it is the change that later makes per-identity rate limiting and quotas
safe at the edge.

## The split: authenticate at the edge, authorize at the service

- **Authentication** — "is this a valid, unexpired, correctly-signed token from our
  issuer?" — **moves to the gateway.** Unauthenticated traffic is rejected at the edge
  before it consumes a backend or traverses the mesh; the token is validated once, not
  re-validated in every service.
- **Authorization** — "may *this* identity perform *this* action on *this* resource?" —
  **stays at the service.** Only the service knows its resource model, and keeping it there
  means a gateway compromise does not grant backend access. This preserves the ADR-0032
  defense-in-depth rather than replacing it.

The gateway is therefore **not** the sole gatekeeper. It is the first gate; the service
remains the last one, and trusts the gateway's verdict only because it arrives over the
mesh (mTLS-proven `X-Service-Identity=gateway`), never from an arbitrary caller.

## Decisions

- **This tier owns per-route and per-identity limits; the ingress security floor
  (ADR-0043) does not.** ADR-0043 is a blanket IP rate limit applied uniformly to every
  edge server as a security measure. Finer limits — per route, per API key, per verified
  client — are application logic keyed on a *verified* identity, which only exists once
  authentication is at the gateway. They are therefore defined and enforced here, on top of
  the IP floor below.

- **The enforcement mechanism for auth (and identity-keyed limits) is an open choice
  between two shapes, both compatible with the stock-nginx render.** (a) `auth_request` →
  a co-located auth service (below), or (b) a **custom nginx dynamic module** (compiled,
  e.g. via `ngx-rust`) that validates the token and enforces per-route/identity limits
  inline — no per-request subrequest, richest control, but it owns a module built against
  each nginx version (the version-coupling cost that ruled out ModSecurity in ADR-0043).
  The `auth_request` shape is the lower-risk default and the one the rest of this ADR
  details; the dynamic module is the higher-ceiling alternative if the subrequest hop or
  the split enforcement point ever bites. This choice is deliberately left open here.

- **nginx stays the router; authentication is added via the built-in `auth_request`
  directive — no lua/njs JWT logic in nginx.** nginx is an excellent TLS-terminating,
  path-routing edge and is already rendered deterministically (`internal/nginx`,
  ADR-0043). `auth_request` issues an internal subrequest to an auth endpoint before the
  content phase: a `2xx` admits the request (and nginx forwards headers the auth service
  returns), a `401/403` rejects it at the edge, any other status (including auth-service
  unavailability) fails the request `500`. Token validation lives in a service behind that
  seam, not in a per-request JS/lua blob inside the router. Rejected alternatives (njs JWT,
  a full market gateway) are recorded below.

- **The auth service is co-located on each gateway host and reached over loopback (or a
  unix socket), not through the mesh.** `auth_request` fires once per incoming request, so
  the auth service's latency and availability gate *every* request. A same-host loopback
  call (or a unix-domain socket — no TCP port to collide or firewall) keeps that to
  microseconds; routing the auth subrequest through the mesh would add an mTLS handshake
  and two proxy hops to the hot path of every request. This mirrors the existing trust
  boundary: the gateway already speaks only to `127.0.0.1:<GatewayEgressPort>` (its local
  mesh proxy) and trusts it because they share a host. The auth service may still be a mesh
  member for its *own outbound* needs (fetching JWKS from the issuer, calling an identity
  service), cached in-memory so it is not a per-request outbound; only the inbound
  `auth_request` path is loopback.

- **Build the auth service in Rust rather than adopting a market gateway.** The validation
  surface is small and bounded — verify the token with a vetted library (`jsonwebtoken`,
  which handles the crypto that is dangerous to hand-roll: `alg:none`, algorithm
  confusion, signature verification), check `iss`/`aud`/`exp`/`nbf` with clock skew, cache
  JWKS with rotation, emit the verified-identity headers, return `200`/`401`. This fits the
  project's build-and-own posture (inforge, the mesh, the PKI, and the agent are all
  in-house), reuses existing Rust (wardnet-cloud) and can share the daemon PKI trust model,
  and deploys as just another co-located mesh member inforge already knows how to run. Auth
  here is simpler than the mTLS mesh + PKI already operated. Ory Oathkeeper is the
  considered off-the-shelf fallback (see below).

- **Verified identity crosses to the service as headers, set explicitly.** The auth
  subrequest's response headers are not auto-forwarded. nginx captures them with
  `auth_request_set $var $upstream_http_<header>` and re-emits them onto the real upstream
  request via `proxy_set_header` (e.g. `X-Auth-Subject`, plus any needed claim headers).
  The service trusts these **only** because they arrive over the mesh from the gateway leaf
  — a caller cannot inject them directly (the mesh admits only allowlisted peers, and the
  gateway is a distinct mesh identity). The exact header set is a contract to fix when this
  is implemented; `Authorization` continues to be forwarded so the service can re-derive
  claims if it wishes.

- **Rate limiting on a verified claim requires a second pass or auth-service enforcement —
  a single-pass `limit_req` on an auth-set header silently does nothing.** nginx evaluates
  `limit_req`/`limit_conn` in the **preaccess** phase, which runs *before* `auth_request`
  (the **access** phase). On one pass the rate-limit key is read before the auth subrequest
  runs, so a key referencing an auth-set header is empty — and nginx skips a limit whose
  key is empty (it fails *open*). Two supported ways to actually key on a verified
  identity:
  1. **Two-pass nginx** — the front server authenticates and stamps `X-Auth-Subject`, then
     `proxy_pass`es to an internal loopback server whose preaccess `limit_req` keys on that
     now-present header. One extra internal hop, idiomatic here (the ssl_preread→loopback
     terminators and gateway→mesh egress are the same shape).
  2. **Enforce per-identity quotas in the auth service** — it already holds the verified
     claims; it checks the counter and returns `429` from the subrequest. This is preferred
     for anything richer than a leaky bucket (per-plan quotas, sliding windows) and is the
     only option that scales to a global limit across nodes (next decision).
  The ADR-0043 IP/volumetric layer stays in nginx preaccess, independent of auth.

- **HA: separate limits by their state requirement — do not give nginx a global brain.**
  nginx `limit_req`/`limit_conn` zones are per-host shared memory (across workers on one
  box, not across hosts), so N gateway nodes enforce N independent limits. The design
  splits accordingly:
  - **Volumetric / IP limits stay per-node and approximate** — for flood protection, exact
    global counting does not matter; per-node caps still stop a source from running wild,
    and CrowdSec's nftables bans (ADR-0043) are the fleet backstop.
  - **Per-identity quotas move to a shared store** — the auth service (which runs on every
    gateway node and holds the verified identity) checks a **Redis-backed token bucket /
    sliding window**, atomic via a Lua script, keyed by the verified client id. HA changes
    only the *backing* of the counter (in-process → Redis), not the `auth_request`→`429`
    shape.
  - **CrowdSec bans move to a central/shared LAPI** — one node runs the local API, others
    run agent+bouncer against it, so ban decisions are fleet-wide; the community blocklist
    is already shared.
  - **Failure posture**: the shared quota store is on the hot path, so quota checks **fail
    open** — a store blip must not `503` the API — and the per-node nginx IP cap still
    stands as the volumetric backstop. The two layers back each other up.

## Request flow (target)

```
client ──TLS──▶ gateway nginx ──loopback──▶ auth service        (200 + X-Auth-Subject | 401)
                     │                             │ (validates JWT; JWKS cached;
                     │                             │  optional per-identity quota via Redis)
                     └─(if 200, +X-Auth-Subject)──loopback──▶ mesh egress ──mTLS──▶ service
                                                                          (trusts identity via
                                                                           mesh; enforces authZ)
```

## Consequences

- A new co-located daemon on every gateway host (the auth service), deployed and
  supervised like the mesh proxy — `Restart=always` per `daemon-units-restart-on-any-exit`,
  since a dead auth service fails all gateway traffic closed.
- The gateway nginx render (`gatewayServer`, `internal/nginx`) gains the `auth_request`
  location and the `auth_request_set`/`proxy_set_header` identity plumbing; the two-pass
  variant adds an internal server when identity-keyed rate limiting is enabled.
- A new hot-path dependency on the issuer's JWKS (cached, rotated) for the auth service,
  and — only when global per-identity quotas are enabled — on a shared store (Redis).
- The ADR-0043 note "gateway auth unlocks identity keying" is made precise: the unlock is a
  *verified identity existing at the edge*, enforced via a second nginx pass or in the auth
  service — not `limit_req` suddenly seeing the claim on one pass.
- Authorization stays distributed across services; this ADR deliberately does not
  centralize policy. A future policy-decision layer (if ever wanted) is out of scope.

## Considered and rejected

- **JWT validation inside nginx (njs or lua).** njs is available in the nginx.org repo and
  can decode/verify a token, but it puts security-critical crypto in a per-request script
  inside the router — hard to test, clutters the deterministic render, and owns JWKS
  rotation / signature edge cases in the wrong place. `auth_request` to a real service is
  cleaner and keeps nginx doing what it is good at.
- **A full market API gateway (Kong / Tyk / APISIX / Traefik).** Replaces the clean,
  self-rendered nginx edge with a new runtime and config dialect and a larger operational
  surface, to buy capabilities obtainable from `auth_request` + a small service. A step away
  from the architecture, not toward it.
- **Ory Oathkeeper (kept as the buy fallback).** A purpose-built identity-&-access decision
  proxy that fits the `auth_request` pattern and is config-driven. Reasonable if owning
  token validation is undesirable; declined as the default only because the custom Rust
  service is a small, well-understood surface consistent with everything else in-house.
- **Making the gateway the sole authenticator/authorizer.** Rejected — it would create a
  single bypass point and discard the ADR-0032 service-side checks. The gateway
  authenticates; the service still authorizes.
- **Global rate limiting inside nginx.** Not possible without an external store and lua/njs;
  the shared-state need is met in the auth service instead, where the verified identity and
  a Redis client already naturally live.
