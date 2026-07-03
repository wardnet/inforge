# East-west service traffic runs through a derived per-host mesh, distinct from the north-south ingress and gateway

An earlier iteration (wardnet-cloud ADR-0013) modelled service-to-service authentication as a single
central **API gateway**: every service dialed one `INFORGE_GATEWAY_URL`, the gateway terminated the
caller's mTLS, checked a per-route allow-list, and injected an identity header. Working through it
surfaced a conflation: that "gateway" was doing **east-west** work (service identity, `allowed_services`,
per-peer authz) while wearing a **north-south** name, and it baked in a **central hairpin** topology —
one URL every caller dials, a scope-singleton everyone routes through. Two problems followed:

1. **It mixed two planes.** North-south (external **daemons** dialing in, authenticated by their own
   app-level JWT) and east-west (services calling services, authenticated by mesh identity) are different
   callers with different auth. inforge already has the north-south primitive — `ingress` (ADR-0026). The
   missing piece is a clean **east-west** primitive, not another gateway.
2. **It couldn't absorb topology change.** The first thing wardnet scales out is splitting co-located
   services (`ddns` + `tunneller`) onto separate hosts. With a central gateway URL, that's fine; but the
   deeper requirement is **location transparency** — a caller must not change code, manifest, or URL because
   a callee moved hosts. A model where callers address a *location* (or a central relay) bakes the
   single-host assumption in.

We decided to introduce an **east-west service mesh** as its own plane: a **per-host mesh proxy** (a
second nginx, private) that every service reaches at a stable local endpoint, plus a **global-scope mesh
gateway** for cross-scope hops. It is **derived**, not authored. The north-south planes (`ingress` for
apps/web, a reframed `gateway` for daemons) stay separate. This supersedes the "central single gateway /
`type: gateway` route / signed-assertion" framing before any of it shipped.

## Roles (three primitives, two planes)

| Primitive | Plane | Public? | Authored? | Purpose |
|---|---|---|---|---|
| `ingress` (ADR-0026) | north-south | yes | yes | apps (SPAs) + per-service web/SNI routes |
| `gateway` | north-south | yes | yes | the public edge **daemons** HTTPS into |
| **mesh** | east-west | no (global gateway excepted) | **no — derived** | service ↔ service |

## The mesh is derived, not a resource

A service is already a mesh member by declaring `pki:` (ADR-0024). inforge **materializes** a mesh proxy
on any host running ≥1 `pki:` service and **generates** its routing table from the set of pki-services and
their hosts — exactly as nginx ingress config is derived, never authored. There is **no** mesh resource,
no `host:` FK, no scope-singleton. The only authored east-west surface is per-service (see Authoring).

## Location transparency (the addressing contract)

A service **never addresses a callee's location**. inforge injects `INFORGE_MESH_URL` per service, pointing
at that host's **local** mesh proxy; the **target is the first path segment** (the canonical service name):

```
GET $INFORGE_MESH_URL/tunneller/api/foo      # ddns calling tunneller
```

The local mesh owns the routing table and resolves the target's **current** location — loopback (co-located),
private IP (same region, other host), or the global mesh gateway (cross-scope). When a service scales onto its
own host, inforge **regenerates the local mesh routing tables**; the caller's URL is byte-identical. Zero app
or manifest change. `$INFORGE_MESH_URL` also carries the caller's identity to the local mesh (see below), so
each service gets its own value.

## The app ↔ local-mesh hop: plain HTTP (v1)

The service talks **plain HTTP over TCP loopback** to its local mesh, both directions — outbound to
`$INFORGE_MESH_URL`, inbound from the mesh to `127.0.0.1:<mesh.port>`. **No TLS in the service**; there is no
certificate on this hop, so the `127.0.0.1`-vs-cert problem never arises. inforge assigns each service a
distinct **egress port** on the local mesh (what `$INFORGE_MESH_URL` points at); the mesh maps that port →
caller identity, and presents the caller's leaf on the onward mTLS hop.

**Caller identity here is by egress port, which a co-tenant process can spoof** (any process on the host can
dial `127.0.0.1:<port>`). We accept this for v1 as a **conscious, documented risk**: services are first-party
and few, and today's co-located pair mutually trust each other. The **hardening tripwire** — switch each
service's egress endpoint to a **per-service unix socket** in a `0700` directory owned by the service user
(unspoofable via filesystem perms; the app's HTTP client gains a unix-socket transport) — fires when either
(a) co-located services stop mutually trusting each other, or (b) the gateway co-locates with lower-trust
services. That change is contained (one app client module + inforge provisioning a dir) and does not touch the
authoring surface or URLs.

## The mesh ↔ mesh hop: mTLS, no assertion

When a target is on another host, the local mesh originates **mTLS** to the target host's mesh, **presenting
the caller's leaf** (`CN=<scope>/<service>`, ADR-0024 as amended below). The target mesh verifies the client
cert against the plaintext trust bundle, checks the callee's `allowed_services`, and forwards to the local
service over loopback plain HTTP, injecting the caller identity as a header. The handshake **is** the identity
proof — there is **no signed assertion / JWT / shared secret**. This obsoletes the signer design that a
central gateway would have needed (it existed only to carry identity across a plain-HTTP central-gateway→app
hop that no longer exists).

**Leaf custody shifts:** because the mesh presents the caller's leaf on egress, the **mesh proxy holds the
co-located services' leaf keys** — the leaf moves from the service's own tmpfs (today) to the mesh nginx. The
service holds **no** cert material for east-west (it speaks plain HTTP both ways).

## Identity and authz

- **Leaf CN becomes `<scope>/<service>`** (was `<service>`) — the callee mesh authorizes on the client cert
  identity. Already landed (`meshcert.MintLeaf`). The URI SAN remains the canonical SPIFFE identity.
- **`allowed_services` is callee-side** — a service declares who may call it; enforced at the **callee's**
  local mesh (a disallowed caller is rejected there, never reaching the service). Bare service names, expanded
  to scope-qualified identities by the existing TrustSet math. Empty = no service peers.

## Cross-scope: the global-only mesh gateway

Networks are segregated, so a regional mesh cannot reach a global host's private IP — cross-scope east-west
**must** traverse a public endpoint. Only the **global** scope exposes one: a **public mTLS mesh gateway**. A
regional mesh dials it over the internet, presenting the caller's leaf, SNI-naming the target. When the global
scope spans >1 host it does **`ssl_preread` SNI L4 passthrough** (ADR-0027) to the target global host's mesh —
mTLS stays end-to-end, the gateway never holds keys; on a single global host it terminates locally.

**Regional scopes are private-only** — never a public mesh listener. This structurally enforces the direction
rule (regional→global only): a regional service has no public east-west endpoint to receive on, so global can
never initiate to it. "Global is the special region others can reach" falls out of the asymmetry.

## The north-south gateway (daemon edge)

`gateway` is kept, reframed as the public host **daemons HTTPS into**. It is a **mesh client**: a daemon
request is TLS-terminated at the gateway, path-routed, and handed to the target service **through the mesh**
(so the gateway is location-transparent too and needs no knowledge of where services live). The gateway has
mesh identity `<scope>/gateway`, so a daemon-facing service simply lists `gateway` in its `allowed_services`.

The **daemon's own JWT is not validated at the gateway** — the Ed25519 PoP JWT is request-bound and app-bound,
so the gateway forwards it through and the **service** validates it. A service thus demuxes inbound traffic:
caller `<scope>/gateway` ⇒ daemon-originated, validate the forwarded daemon JWT; caller `<scope>/<service>` ⇒
a service peer, already authenticated by the mesh.

The gateway declares its own `routes: [{ path, service }]` (its external API surface); services no longer
declare gateway routes.

## Two nginx per host (public north-south + private mesh)

A host runs **two** nginx, split by trust direction — they do **not** merge:

- **north-south nginx (public):** `ingress` routes + apps + the `gateway` + health. Binds `0.0.0.0:443/80`.
  (The earlier "gateway and ingress merge into one nginx" holds — that merge is *here*, both being north-south.)
- **east-west nginx (mesh):** per-service egress endpoints + mesh↔mesh mTLS + routing table. On **regional**
  hosts binds **only loopback + the private network**; on the **global** host it adds the one public
  mesh-gateway listener.

Rationale: the mesh nginx holds the service leaf keys and makes the east-west authz decisions — the crown
jewel — so it must not share a process with the most-attacked (internet-facing) surface. Two processes make
"the mesh is not internet-reachable" a structural fact. The configs (mTLS + egress endpoints + routing vs.
public ACME + SNI + path routing) are different enough that two focused configs are cleaner than one
mega-config. Cost: a second systemd unit + config per host, which inforge generates.

## Authoring surface

Per-service `mesh:` block (callee-side); gateway `routes:`:

```yaml
# service manifest
mesh:
  port: 8080                          # backend port the service serves mesh traffic on (loopback, plain HTTP)
  allowed_services: [tunneller, gateway]   # who may call me over the mesh

# gateway manifest
name: api
host: bridge
subdomain: api                        # -> api.<slug>.<base>
routes:
  - { path: /ddns/,   service: ddns }
  - { path: /tunnel/, service: tunneller }
```

**Removed:** the slice-2 `GatewaySpec`-as-east-west shape, the `type: gateway` route, the single
`INFORGE_GATEWAY_URL` / scope-singleton framing. **Validation:** a gateway `route → <svc>` requires that
`<svc>` list `gateway` in its `allowed_services` (the two sides must agree).

## WAF: deferred, gateway-only

A WAF (e.g. ModSecurity/CRS) inspects hostile external input — a north-south concern that belongs on the
**public gateway nginx**, never the mesh. Deferred for v1 (low value over JWT-authed first-party JSON daemon
traffic; false-positive tuning burden; a paid third-party compiled module breaks "stock nginx"). Reserved as a
future opt-in `waf:` capability on the `gateway` resource.

## Considered options

- **Per-service sidecar mesh (Istio/Linkerd) or eBPF (Istio Ambient).** These get "free" caller identity from
  **network-namespace isolation** (pods) or kernel capture — machinery we don't have (services are systemd
  units on a shared loopback) and don't want to build. Our per-host shared proxy + explicit addressing is the
  proportionate model; per-service unix sockets are the filesystem-namespace analog of what pods give.
- **Central relay (the original single-gateway hairpin).** Simpler to configure but bakes centralization into
  the authoring surface (one URL, a scope-singleton) and is a bottleneck/SPOF; rejected for location
  transparency.
- **Unix sockets for app↔mesh from day one.** Unspoofable, but adds inforge per-service dir provisioning and a
  Rust unix-socket connector now; deferred to the hardening tripwire above.
- **App presents its leaf (client mTLS) to the local mesh.** Unspoofable over plain TCP and `reqwest`-native,
  but reintroduces client-side TLS in the app (the thing the mesh removes) and either duplicates the leaf key
  (app + mesh) or revives the assertion on the mesh→mesh hop. Rejected — it hands back the "no TLS in
  services" property to solve a risk we accept for v1.
- **One nginx for both planes.** Simpler process count, but co-locates the internet-facing surface with the
  mesh's leaf keys and authz. Rejected for privilege separation.

## Realization invariants (for the mesh/gateway nginx generator, S3)

- **Every mesh and gateway L7 proxy location must be WebSocket-capable.** A WS `Upgrade` handshake
  only tunnels through nginx with `proxy_http_version 1.1`, `Upgrade`/`Connection` passthrough (the
  module-level `map $http_upgrade $connection_upgrade`), and a generous `proxy_read_timeout`. A daemon
  WS (e.g. tunneller `/v1/tunnel`) crosses up to three proxy hops (gateway → gateway-mesh → callee-mesh),
  each of which needs it; the mTLS mesh↔mesh hop carries WS unchanged (TLS is transport, the headers ride
  inside). The mesh is a general HTTP/WS proxy, so this is applied uniformly (no per-route WS flag). Raw
  L4 data planes (`forward` / `ssl_preread` stream) are separate and unaffected.
- **No proxy hop rewrites the request path** (gateway path-preserving; mesh routes the target out-of-band),
  so a daemon's PoP-signed path reaches the service byte-for-byte. The gateway route `path:` is the
  daemon-facing prefix the service mounts under, not a stripped alias.
- **The gateway stamps the real daemon IP into `X-Forwarded-For`**, preserved through the mesh, so a
  callee's per-IP logic keyed on its loopback peer keeps working.

## Consequences / rollout

- Slice 1 (leaf `CN=<scope>/<service>`) and the `inforge-agent` binary rename stand unchanged.
- Slice 2's schema is **reshaped**: drop `GatewaySpec`-as-east-west + `type: gateway`; add the per-service
  `mesh:` block, keep `allowed_services` (now under `mesh:`), and reframe `gateway` as the north-south daemon
  edge with `routes: [{path, service}]`.
- New realization: the **mesh nginx** (per-host, second unit) — egress endpoints, routing table, mesh↔mesh
  mTLS, the global mesh gateway; leaf-key delivery to the mesh nginx; `INFORGE_MESH_URL` injection.
- wardnet-cloud: services become **pure plain-HTTP** for east-west (delete the rustls mesh **and** the app's
  own leaf handling for inbound/outbound); add an inbound auth demux (mesh identity header ⇒ peer; forwarded
  daemon JWT ⇒ daemon). See the companion integration note.
