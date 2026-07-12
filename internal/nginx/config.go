package nginx

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/pathglob"
	"github.com/wardnet/inforge/internal/types"
)

// dir is a small constructor for a simple (block-less) nginx directive.
func dir(name string, args ...string) *crossplane.Directive {
	return &crossplane.Directive{Directive: name, Args: args}
}

// block is a constructor for a block directive (it carries child directives, so
// crossplane renders it with braces rather than a trailing semicolon).
func block(name string, args []string, children ...*crossplane.Directive) *crossplane.Directive {
	return &crossplane.Directive{Directive: name, Args: args, Block: crossplane.Directives(children)}
}

// mixedPort describes one public listen port shared by tls-termination/app
// servers AND a single forward (passthrough) service. nginx cannot bind the same
// public port in both http{} (ssl) and stream{}, so the public socket moves to a
// stream{} ssl_preread server that demuxes by SNI: every known FQDN routes to an
// internal loopback terminator (the moved http server), the unknown SNI to the
// forward backend (the map default — why a port admits at most one forward).
type mixedPort struct {
	listen   int                // public port
	loopback int                // 127.0.0.1 port the moved terminators listen on
	fqdns    []string           // sorted known SNIs on this port (tls-termination + app FQDNs)
	forward  types.IngressRoute // the single forward (the map default)
}

// Render builds the complete nginx.conf for a host from its ingress routes, apps,
// and service health endpoints. The output is deterministic: every server list is
// sorted (routes by (listen, service), apps/health by FQDN, mixed ports by listen)
// so the same input always renders the same bytes.
//
// A public listen port shared by a tls-termination/app server AND a forward is a
// "mixed" port: its http servers move to internal 127.0.0.1 terminators and a
// stream{} ssl_preread server fronts the public port, fanning known SNIs to the
// terminators (carrying the client address with the PROXY protocol so set_real_ip
// recovers it) and the unknown SNI to the forward backend. Non-mixed ports render
// as before (tls-termination → http listen ssl; forward → plain stream server).
//
// health entries render as plain-HTTP http{} servers on healthPort, demuxed
// strictly by server_name (the service FQDN) and reverse-proxied to the backend
// health port. http{} is emitted when the host terminates TLS for any route, serves
// any app, or has any health endpoint; stream{} when it has any forward route.
func Render(routes []types.IngressRoute, apps []types.IngressApp, health []types.IngressHealth, healthPort int, gateways []types.IngressGateway) (string, error) {
	var terminate, forward []types.IngressRoute
	for _, r := range routes {
		// Backend is the resolved upstream address the caller must fill — "127.0.0.1"
		// for a co-located route, the backend's private IP for a cross-host one. An
		// empty Backend means the program/provider wiring failed to resolve it; render
		// must fail loud rather than silently proxy the service to localhost.
		if r.Backend == "" {
			return "", fmt.Errorf("nginx: ingress route for service %q has no backend address (unresolved upstream)", r.Service)
		}
		switch r.Type {
		case types.IngressTypeTLSTermination:
			terminate = append(terminate, r)
		case types.IngressTypeForward:
			forward = append(forward, r)
		default:
			return "", fmt.Errorf("nginx: ingress route for service %q has unknown type %q", r.Service, r.Type)
		}
	}
	byListenService := func(rs []types.IngressRoute) func(i, j int) bool {
		return func(i, j int) bool {
			if rs[i].Listen != rs[j].Listen {
				return rs[i].Listen < rs[j].Listen
			}
			return rs[i].Service < rs[j].Service
		}
	}
	sort.Slice(terminate, byListenService(terminate))
	sort.Slice(forward, byListenService(forward))

	// Apps render in a stable order independent of input order. Each app must carry
	// a document root — an empty Root means the program wiring failed to resolve it,
	// and nginx would serve the whole filesystem; fail loud instead.
	sortedApps := append([]types.IngressApp(nil), apps...)
	for _, a := range sortedApps {
		if a.Root == "" {
			return "", fmt.Errorf("nginx: ingress app %q has no document root", a.Name)
		}
	}
	sort.Slice(sortedApps, func(i, j int) bool { return sortedApps[i].FQDN < sortedApps[j].FQDN })

	// Health endpoints render in a stable order. Each must carry a resolved backend
	// (like a route) or nginx would proxy health to nowhere; fail loud.
	sortedHealth := append([]types.IngressHealth(nil), health...)
	for _, h := range sortedHealth {
		if h.Backend == "" {
			return "", fmt.Errorf("nginx: ingress health for service %q has no backend address (unresolved upstream)", h.Service)
		}
		// The health listener is allowlist-only (ADR-0034). Failing loud — not
		// falling back to a full-open location — keeps the property at the
		// enforcement layer: a pathless entry reaching the render (a deploy that
		// skipped validation, e.g. a pre-ADR-0034 manifest) must never silently
		// proxy the whole backend port to the internet.
		if len(h.Paths) == 0 {
			return "", fmt.Errorf("nginx: ingress health for service %q declares no probe paths; the health listener is allowlist-only (declare health_probe_paths)", h.Service)
		}
	}
	if len(sortedHealth) > 0 && healthPort <= 0 {
		return "", fmt.Errorf("nginx: ingress has health endpoints but no public health port")
	}
	sort.Slice(sortedHealth, func(i, j int) bool { return sortedHealth[i].FQDN < sortedHealth[j].FQDN })

	// Gateways render in a stable order. Each must carry its resolved FQDN — the
	// server_name and ACME cert SNI; empty means the program wiring failed. Every
	// route pattern is compiled up front (validation guarantees parseability; a
	// bad glob reaching this far fails the render loud rather than emitting a
	// broken location).
	sortedGateways := append([]types.IngressGateway(nil), gateways...)
	gatewayRegexOf := map[string]string{}
	for _, g := range sortedGateways {
		if g.FQDN == "" {
			return "", fmt.Errorf("nginx: gateway %q has no FQDN", g.Name)
		}
		for _, rt := range g.Routes {
			p, err := pathglob.Parse(rt.Pattern)
			if err != nil {
				return "", fmt.Errorf("nginx: gateway %q route for service %q: invalid path glob %q: %w", g.Name, rt.Service, rt.Pattern, err)
			}
			gatewayRegexOf[rt.Pattern] = p.Regex()
		}
	}
	sort.Slice(sortedGateways, func(i, j int) bool { return sortedGateways[i].FQDN < sortedGateways[j].FQDN })

	// Classify ports: a forward whose listen also carries a tls-termination route
	// or (on 443) an app is "mixed" and needs ssl_preread; the rest are forward-only.
	forwardByListen := map[int]types.IngressRoute{}
	for _, f := range forward {
		if prev, dup := forwardByListen[f.Listen]; dup {
			return "", fmt.Errorf("nginx: two forward routes share listen %d (services %q and %q); a forward port is single-service-exclusive", f.Listen, prev.Service, f.Service)
		}
		forwardByListen[f.Listen] = f
	}
	termOnListen := map[int]bool{}
	for _, t := range terminate {
		termOnListen[t.Listen] = true
	}
	// A forward port is mixed when it also carries a tls-termination route, an app,
	// or a gateway. Apps and gateways are always served on :443 (appServer and
	// gatewayServer hardcode listenDir(443)), so they can only make :443 mixed —
	// never another port. If either ever gains a configurable port, this 443
	// literal must move in lockstep.
	mixedListens := map[int]bool{}
	for listen := range forwardByListen {
		if termOnListen[listen] || (listen == 443 && (len(sortedApps) > 0 || len(sortedGateways) > 0)) {
			mixedListens[listen] = true
		}
	}
	// Assign a deterministic loopback port per mixed public port (ascending).
	mixedSorted := sortedIntKeys(mixedListens)
	if len(mixedSorted) > MaxMixedPorts {
		return "", fmt.Errorf("nginx: %d mixed listen ports exceeds the reserved loopback range of %d", len(mixedSorted), MaxMixedPorts)
	}
	loopbackOf := map[int]int{}
	for i, p := range mixedSorted {
		loopbackOf[p] = LoopbackBase + i
	}
	mixedPorts := make([]mixedPort, 0, len(mixedSorted))
	for _, p := range mixedSorted {
		var fqdns []string
		for _, t := range terminate {
			if t.Listen == p {
				fqdns = append(fqdns, t.FQDNs...)
			}
		}
		if p == 443 {
			for _, a := range sortedApps {
				fqdns = append(fqdns, a.FQDN)
			}
			for _, g := range sortedGateways {
				fqdns = append(fqdns, g.FQDN)
			}
		}
		sort.Strings(fqdns)
		mixedPorts = append(mixedPorts, mixedPort{listen: p, loopback: loopbackOf[p], fqdns: fqdns, forward: forwardByListen[p]})
	}
	var forwardOnly []types.IngressRoute
	for _, f := range forward {
		if !mixedListens[f.Listen] {
			forwardOnly = append(forwardOnly, f)
		}
	}

	// listenDir returns the listen directive for a tls-termination/app server on
	// public port p: a plain public ssl listen, or — when p is mixed — a loopback
	// ssl listen that accepts the PROXY protocol from the ssl_preread fronting it.
	listenDir := func(p int) *crossplane.Directive {
		if lp, ok := loopbackOf[p]; ok {
			return dir("listen", fmt.Sprintf("127.0.0.1:%d", lp), "ssl", "proxy_protocol")
		}
		return dir("listen", strconv.Itoa(p), "ssl")
	}

	var top crossplane.Directives
	top = append(top,
		dir("load_module", acmeModule),
		dir("user", "nginx"),
		dir("worker_processes", "auto"),
		dir("pid", "/run/nginx.pid"),
		block("events", nil, dir("worker_connections", "1024")),
	)

	if len(terminate) > 0 || len(sortedApps) > 0 || len(sortedHealth) > 0 || len(sortedGateways) > 0 {
		top = append(top, httpBlock(terminate, sortedApps, sortedHealth, healthPort, sortedGateways, gatewayRegexOf, listenDir, len(mixedPorts) > 0))
	}
	if len(forwardOnly) > 0 || len(mixedPorts) > 0 {
		top = append(top, streamBlock(forwardOnly, mixedPorts))
	}

	var sb strings.Builder
	if err := crossplane.Build(&sb, crossplane.Config{Parsed: top}, &crossplane.BuildOptions{Indent: 4}); err != nil {
		return "", fmt.Errorf("nginx: build config: %w", err)
	}
	// crossplane strips the trailing newline; restore it and prepend the managed
	// banner so the rendered file is a stable, attributable artifact.
	return "# Managed by inforge — do not edit by hand.\n" + sb.String() + "\n", nil
}

// httpBlock renders the http{} context: the ACME issuer + shared zone (only when
// something terminates TLS), the real-ip recovery directives (only when a mixed
// port routes through ssl_preread + the PROXY protocol), one server per
// tls-termination route, one per app, the health port's default_server 404
// catch-all plus one plain-HTTP server per health endpoint, and the :80
// ACME-challenge/redirect server (only when something terminates TLS).
// listenDir places a terminating server on its public port or, when that port is
// mixed, on its internal loopback port.
func httpBlock(terminate []types.IngressRoute, apps []types.IngressApp, health []types.IngressHealth, healthPort int, gateways []types.IngressGateway, gatewayRegexOf map[string]string, listenDir func(int) *crossplane.Directive, anyMixed bool) *crossplane.Directive {
	terminatesTLS := len(terminate) > 0 || len(apps) > 0 || len(gateways) > 0

	// Content-Type for everything served off disk. Unconditional: nginx's built-in
	// default_type is text/plain, so without this an app's JS bundle is labelled
	// text/plain and the browser refuses to execute it as a module. The
	// application/octet-stream fallback (nginx's own convention) keeps an unmapped
	// extension a byte stream rather than something a browser might render.
	children := crossplane.Directives{
		dir("include", mimeTypesPath),
		dir("default_type", "application/octet-stream"),
	}
	if len(gateways) > 0 {
		// WebSocket upgrade support for the gateway's mesh-bound locations: a daemon
		// WS handshake crosses gateway → gateway-mesh → callee-mesh, and every hop
		// must pass Upgrade through (ADR-0032 realization invariant).
		children = append(children,
			block("map", []string{"$http_upgrade", "$connection_upgrade"},
				dir("default", "upgrade"),
				dir("''", "close"),
			),
		)
	}
	if terminatesTLS {
		children = append(children,
			dir("resolver", strings.Fields(resolverAddrs)...),
			block("acme_issuer", []string{acmeIssuer},
				dir("uri", acmeDirectoryURL),
				dir("state_path", acmeStatePath),
				dir("accept_terms_of_service"),
			),
			dir("acme_shared_zone", "zone=ngx_acme_shared:1M"),
		)
	}
	if anyMixed {
		// The ssl_preread server fronts mixed ports and forwards the PROXY protocol
		// to the loopback terminators; recover the real client address from it.
		children = append(children,
			dir("set_real_ip_from", "127.0.0.1"),
			dir("real_ip_header", "proxy_protocol"),
		)
	}
	for _, r := range terminate {
		children = append(children, terminateServer(r, listenDir(r.Listen)))
	}
	for _, a := range apps {
		children = append(children, appServer(a, listenDir(443)))
	}
	for _, g := range gateways {
		children = append(children, gatewayServer(g, listenDir(443), gatewayRegexOf))
	}
	if len(health) > 0 {
		children = append(children, healthCatchAllServer(healthPort))
	}
	for _, h := range health {
		children = append(children, healthServer(h, healthPort))
	}
	if terminatesTLS {
		// The :80 server is required for ACME HTTP-01: the module intercepts
		// /.well-known/acme-challenge/ before location matching, so the catch-all
		// redirect to HTTPS does not swallow challenges.
		children = append(children, block("server", nil,
			dir("listen", "80"),
			block("location", []string{"/"},
				dir("return", "301", "https://$host$request_uri"),
			),
		))
	}
	return block("http", nil, children...)
}

// terminateServer renders one tls-termination server: ACME-managed TLS for the
// route's SNIs, reverse-proxying cleartext to the backend target. listen is the
// pre-computed listen directive (public ssl, or a mixed-port loopback ssl listen
// accepting the PROXY protocol). Render has already verified r.Backend is non-empty.
func terminateServer(r types.IngressRoute, listen *crossplane.Directive) *crossplane.Directive {
	serverName := append([]string{}, r.FQDNs...)
	return block("server", nil,
		listen,
		dir("server_name", serverName...),
		dir("acme_certificate", acmeIssuer),
		dir("ssl_certificate", "$acme_certificate"),
		dir("ssl_certificate_key", "$acme_certificate_key"),
		dir("ssl_certificate_cache", "max=2"),
		block("location", []string{"/"},
			dir("proxy_pass", fmt.Sprintf("http://%s:%d", r.Backend, r.Target)),
			dir("proxy_set_header", "Host", "$host"),
			dir("proxy_set_header", "X-Forwarded-For", "$proxy_add_x_forwarded_for"),
			dir("proxy_set_header", "X-Forwarded-Proto", "$scheme"),
		),
	)
}

// appServer renders one app server: ACME-managed TLS for the app's single FQDN,
// serving static files from the app's document root. listen is the pre-computed
// listen directive (public ssl, or a mixed-port loopback ssl listen). A SPA falls
// any non-file path back to /index.html (client-side routing); a non-SPA returns
// 404. Render has already verified a.Root is non-empty.
func appServer(a types.IngressApp, listen *crossplane.Directive) *crossplane.Directive {
	fallback := []string{"$uri", "$uri/", "=404"}
	if a.Spa {
		fallback = []string{"$uri", "$uri/", "/index.html"}
	}
	return block("server", nil,
		listen,
		dir("server_name", a.FQDN),
		dir("acme_certificate", acmeIssuer),
		dir("ssl_certificate", "$acme_certificate"),
		dir("ssl_certificate_key", "$acme_certificate_key"),
		dir("ssl_certificate_cache", "max=2"),
		dir("root", a.Root),
		dir("index", "index.html"),
		block("location", []string{"/"},
			dir("try_files", fallback...),
		),
	)
}

// gatewayServer renders the north-south daemon gateway server (ADR-0032/0034):
// ACME-managed TLS on the gateway's single FQDN, with one regex location per
// derived route (a listed service's public path glob, compiled by pathglob)
// handing the request to the LOCAL mesh proxy's gateway egress listener.
// The location proxies plain HTTP to loopback; the mesh does the mTLS hop
// presenting the gateway's <scope>/gateway leaf, and the callee's mesh stamps
// X-Service-Identity from that cert. Invariants realized here:
//   - the target is named out-of-band in X-Mesh-Target, so the PATH IS PRESERVED
//     byte-for-byte — a daemon's PoP-signed path reaches the service verbatim;
//   - the daemon's Authorization header is forwarded untouched (the service
//     validates the JWT, never the gateway);
//   - every location is WebSocket-capable ($connection_upgrade map, 1h read
//     timeout) — a daemon WS crosses three proxy hops;
//   - the real daemon IP is stamped into X-Forwarded-For at this, the TLS edge;
//   - the gateway's own health probe paths are answered 200 "ok" by nginx
//     itself, over the same TLS path daemons use (edge liveness);
//   - a path matching no route is a JSON 404 at the edge, never proxied.
//
// Regex locations are evaluated in emitted order for every request on this
// server, but validation guarantees the patterns are pairwise non-overlapping
// across the gateway's services (pathglob.Overlaps), so order cannot change
// which route wins. regexOf maps each route's raw pattern to its compiled,
// Render-verified regex.
func gatewayServer(g types.IngressGateway, listen *crossplane.Directive, regexOf map[string]string) *crossplane.Directive {
	routes := append([]types.IngressGatewayRoute(nil), g.Routes...)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Pattern != routes[j].Pattern {
			return routes[i].Pattern < routes[j].Pattern
		}
		return routes[i].Service < routes[j].Service
	})

	children := crossplane.Directives{
		listen,
		dir("server_name", g.FQDN),
		dir("acme_certificate", acmeIssuer),
		dir("ssl_certificate", "$acme_certificate"),
		dir("ssl_certificate_key", "$acme_certificate_key"),
		dir("ssl_certificate_cache", "max=2"),
	}
	probes := append([]string(nil), g.HealthProbePaths...)
	sort.Strings(probes)
	for _, p := range probes {
		children = append(children, block("location", []string{"=", p},
			dir("default_type", "text/plain"),
			dir("return", "200", "ok"),
		))
	}
	for _, rt := range routes {
		children = append(children,
			block("location", []string{"~", regexOf[rt.Pattern]},
				dir("proxy_http_version", "1.1"),
				dir("proxy_set_header", "Upgrade", "$http_upgrade"),
				dir("proxy_set_header", "Connection", "$connection_upgrade"),
				dir("proxy_set_header", "Host", "$host"),
				// SET, never append: this is the internet-facing first hop, so a
				// daemon-supplied X-Forwarded-For is untrusted input — appending
				// ($proxy_add_x_forwarded_for) would forward a forged first entry
				// through the mesh and defeat per-IP audit/rate-limit logic.
				dir("proxy_set_header", "X-Forwarded-For", "$remote_addr"),
				dir("proxy_set_header", "X-Forwarded-Proto", "$scheme"),
				dir("proxy_set_header", "X-Mesh-Target", rt.Service),
				dir("proxy_read_timeout", "3600s"),
				dir("proxy_pass", fmt.Sprintf("http://127.0.0.1:%d", meshpaths.GatewayEgressPort)),
			),
		)
	}
	children = append(children, jsonNotFoundLocation())
	return block("server", nil, children...)
}

// jsonNotFoundLocation is the gateway's catch-all: the gateway fronts REST
// APIs, so an undeclared path is answered with a small JSON error at the edge
// instead of a bare 404 page (ADR-0034).
func jsonNotFoundLocation() *crossplane.Directive {
	return block("location", []string{"/"},
		dir("default_type", "application/json"),
		dir("return", "404", `{"error":"not_found"}`),
	)
}

// healthCatchAllServer renders the health port's explicit default server: it
// answers every request whose Host matches no service's health FQDN with a 404.
// It is REQUIRED, not decorative — with no default_server marked, nginx promotes
// the first server on the port to the implicit default, so an absent or unknown
// Host on the public health port would be proxied straight to that service's
// backend health listener.
func healthCatchAllServer(healthPort int) *crossplane.Directive {
	return block("server", nil,
		dir("listen", strconv.Itoa(healthPort), "default_server"),
		dir("server_name", "_"),
		dir("return", "404"),
	)
}

// healthServer renders one plain-HTTP health server on the ingress's public health
// port, matched strictly by server_name (the service FQDN / request Host) and
// reverse-proxied to the service's backend health port. Every service's health
// shares the one public port, so the Host header is what selects the backend — a
// missing/wrong Host lands on healthCatchAllServer and returns 404. Only the
// service's declared probe paths are proxied (exact match); anything else 404s at
// the listener (ADR-0034). Render has already rejected an entry with no paths
// (allowlist-only, never full-open).
func healthServer(h types.IngressHealth, healthPort int) *crossplane.Directive {
	proxy := func() crossplane.Directives {
		return crossplane.Directives{
			dir("proxy_pass", fmt.Sprintf("http://%s:%d", h.Backend, h.Target)),
			dir("proxy_set_header", "Host", "$host"),
		}
	}
	children := crossplane.Directives{
		dir("listen", strconv.Itoa(healthPort)),
		dir("server_name", h.FQDN),
	}
	paths := append([]string(nil), h.Paths...)
	sort.Strings(paths)
	for _, p := range paths {
		children = append(children, block("location", []string{"=", p}, proxy()...))
	}
	children = append(children, block("location", []string{"/"}, dir("return", "404")))
	return block("server", nil, children...)
}

// streamBlock renders the stream{} context. For each mixed port it emits a
// ssl_preread map ($ssl_preread_server_name → loopback terminator per known SNI,
// the forward backend as the default) and the public server that reads the SNI and
// proxy_passes to the mapped upstream with the PROXY protocol (so both the loopback
// terminators and the forward backend learn the client address). forward-only
// ports keep the plain raw-L4 server.
func streamBlock(forwardOnly []types.IngressRoute, mixed []mixedPort) *crossplane.Directive {
	var children crossplane.Directives
	for _, mp := range mixed {
		entries := make(crossplane.Directives, 0, len(mp.fqdns)+1)
		for _, fqdn := range mp.fqdns {
			entries = append(entries, dir(fqdn, fmt.Sprintf("127.0.0.1:%d", mp.loopback)))
		}
		entries = append(entries, dir("default", fmt.Sprintf("%s:%d", mp.forward.Backend, mp.forward.Target)))
		children = append(children, block("map",
			[]string{"$ssl_preread_server_name", fmt.Sprintf("$ingress_upstream_%d", mp.listen)},
			entries...,
		))
	}
	for _, mp := range mixed {
		children = append(children, block("server", nil,
			dir("listen", strconv.Itoa(mp.listen)),
			dir("ssl_preread", "on"),
			dir("proxy_pass", fmt.Sprintf("$ingress_upstream_%d", mp.listen)),
			dir("proxy_protocol", "on"),
		))
	}
	for _, r := range forwardOnly {
		children = append(children, block("server", nil,
			dir("listen", strconv.Itoa(r.Listen)),
			dir("proxy_pass", fmt.Sprintf("%s:%d", r.Backend, r.Target)),
			dir("proxy_protocol", "on"),
		))
	}
	return block("stream", nil, children...)
}

// sortedIntKeys returns the keys of an int set as an ascending slice.
func sortedIntKeys(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
