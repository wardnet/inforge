---
status: accepted
date: 2026-07-23
issue: "TBD"
---

# The gateway is private, fronted by an ingress

The north-south daemon `gateway` (ADR-0032/0034) was realized as its **own public
edge**: it opened public `443`/`80` on its host, held its own ACME cert and DNS A
record, and terminated client TLS directly. It was a *sibling* of the ingress, not
fronted by it. Two consequences followed: the edge security tier (ADR-0043 CrowdSec +
rate-limit) had to treat gateway hosts as public edges, and there was no way to run a
gateway that is *not* internet-exposed.

We want the opposite invariant: **an API gateway is never exposed on the public
network.** All public traffic enters through the **ingress**, which terminates TLS and
enforces the security tier, then reaches a **private** gateway.

## Decision

A gateway is **always fronted by an ingress**, declared with a **required** `ingress:`
FK (same scope, the scope's singleton ingress — mirroring `app.ingress`). This is a
breaking change to the gateway schema: every gateway manifest must name its ingress.

The gateway renders as **two halves** (Model A — the ingress terminates, it does not
L4-passthrough; only termination lets CrowdSec/rate-limit see the request):

- **Termination server** — on the **fronting ingress host**. ACME-managed TLS on the
  gateway FQDN, the rate limit applied here, reverse-proxying the *whole* FQDN to the
  gateway's routing server. It **blind-proxies**: it never inspects paths or touches
  `X-Mesh-Target` — routing stays gateway business. It participates in ssl_preread
  mixed ports exactly like an app (same SNI-on-443 behavior).
- **Routing server** — on the **gateway's own host**, plain HTTP on
  `nginx.GatewayHTTPPort` (never public; reached only through the ingress). It holds
  the derived `public_paths` routing table and hands each route to the local mesh
  egress. The gateway's mesh membership (identity `<scope>/gateway`, fixed
  `GatewayEgressPort`) is **unchanged** — it still runs on the gateway host.

Co-located (gateway host == ingress host) is the trivial case: both servers render on
one nginx and the hop is loopback. Split (different hosts, same network — enforced by
`checkGateway`) resolves the peer private IPs at deploy and the firewall opens
`GatewayHTTPPort` to the network CIDR.

Derivation changes that fall out:

- **Security tier** — `crowdsecEdgeHosts` derives from **ingress hosts only**; a
  gateway is never a public edge. "Security at the ingress" becomes structural, not a
  config toggle. The rate limit is stamped on the termination server.
- **DNS** — the gateway FQDN A record points at the **ingress host** (where the cert
  lives), not the gateway host.
- **Firewall** — the ingress host opens public `443`/`80` for the gateway; the gateway
  host opens `GatewayHTTPPort` privately (split only). No public port on the gateway.
- **Health** — a listed service's health listener + DNS record relocate to the
  **ingress host** (like a service-with-an-ingress); the gateway's own `/livez` is
  answered by the routing server, reached through the ingress (full-path liveness).

## Client-IP preservation (the load-bearing detail)

wardnet-cloud resolves the client IP from the **leftmost `X-Forwarded-For`**, trusted
**only when the socket peer is loopback** (the mesh proxy) — `client_ip()` in
`crates/common/src/proxy_protocol.rs`, "invariant #8". The old single-server gateway
hard-**set** `X-Forwarded-For = $remote_addr` (anti-spoof: it was the internet-facing
first hop). With an ingress in front, `$remote_addr` at the gateway becomes the ingress,
which would leak the ingress IP to the service.

The fix is entirely inforge-side, **no wardnet-cloud change**:

- The termination server **appends** the real client (`$proxy_add_x_forwarded_for`) —
  it is now the trusted internet-facing hop.
- The routing server recovers the real client with
  `set_real_ip_from <ingress>; real_ip_header X-Forwarded-For; real_ip_recursive on;`
  — trusting **only** the ingress, so a client-forged left entry is stripped
  (anti-spoof preserved) and `$remote_addr` becomes the true client.
- The routing server then **sets** `X-Forwarded-For = $remote_addr` toward the mesh;
  the callee mesh proxy appends, so the service reads the client as the leftmost entry
  — exactly the contract `client_ip()` already expects.

## Alternatives considered

- **Optional FK (gateway may be standalone-public).** Rejected: the security model is
  "never public," and an optional flag leaves a foot-gun (a public-but-undefended
  gateway) and a silent-break path.
- **L4 `ssl_preread` passthrough to a still-terminating gateway (PROXY protocol).**
  Rejected: the ingress could not inspect L7, so CrowdSec/rate-limit could not run on
  gateway traffic — defeating the purpose.
- **Merge the gateway routing into the ingress nginx.** Rejected: it would move the
  gateway's mesh membership/egress onto the ingress host and blur the
  north-south/east-west separation; keeping two servers preserves both.

## Rollout

Phase 1 lands the model with prd gateways **co-located** with their ingresses (unchanged
topology, loopback hop) — full security model + client-IP correctness at minimal risk.
The actual host-split is later a one-line `host:` change; the cross-host machinery
(real_ip, firewall, DNS, peer-IP resolution) is already in place.

Shipped in inforge v6.2.0 (breaking: the required `ingress:` FK). Consumers add the FK
in lockstep with picking up the release.
