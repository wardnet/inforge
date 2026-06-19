package nginx

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
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

// Render builds the complete nginx.conf for a host from its ingress routes and
// apps. The output is deterministic: tls-termination servers, app servers, and
// stream-forward servers are each sorted (routes by (listen, service), apps by
// FQDN), so the same input always renders the same bytes. http{} is emitted when
// the host terminates TLS for any route OR serves any app (an app-only ingress
// still gets the ACME issuer and the :80 challenge/redirect server); stream{} only
// when it has a forward route; a host with none yields a minimal config.
func Render(routes []types.IngressRoute, apps []types.IngressApp) (string, error) {
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

	var top crossplane.Directives
	top = append(top,
		dir("load_module", acmeModule),
		dir("user", "nginx"),
		dir("worker_processes", "auto"),
		dir("pid", "/run/nginx.pid"),
		block("events", nil, dir("worker_connections", "1024")),
	)

	if len(terminate) > 0 || len(sortedApps) > 0 {
		top = append(top, httpBlock(terminate, sortedApps))
	}
	if len(forward) > 0 {
		top = append(top, streamBlock(forward))
	}

	var sb strings.Builder
	if err := crossplane.Build(&sb, crossplane.Config{Parsed: top}, &crossplane.BuildOptions{Indent: 4}); err != nil {
		return "", fmt.Errorf("nginx: build config: %w", err)
	}
	// crossplane strips the trailing newline; restore it and prepend the managed
	// banner so the rendered file is a stable, attributable artifact.
	return "# Managed by inforge — do not edit by hand.\n" + sb.String() + "\n", nil
}

// httpBlock renders the http{} context: the ACME issuer + shared zone, one server
// per tls-termination route, one server per app (static file serving), and the
// :80 server that answers HTTP-01 challenges and redirects everything else to
// HTTPS. Route servers precede app servers; both lists are pre-sorted by Render.
func httpBlock(terminate []types.IngressRoute, apps []types.IngressApp) *crossplane.Directive {
	children := crossplane.Directives{
		dir("resolver", strings.Fields(resolverAddrs)...),
		block("acme_issuer", []string{acmeIssuer},
			dir("uri", acmeDirectoryURL),
			dir("state_path", acmeStatePath),
			dir("accept_terms_of_service"),
		),
		dir("acme_shared_zone", "zone=ngx_acme_shared:1M"),
	}
	for _, r := range terminate {
		children = append(children, terminateServer(r))
	}
	for _, a := range apps {
		children = append(children, appServer(a))
	}
	// The :80 server is required for ACME HTTP-01: the module intercepts
	// /.well-known/acme-challenge/ before location matching, so the catch-all
	// redirect to HTTPS does not swallow challenges.
	children = append(children, block("server", nil,
		dir("listen", "80"),
		block("location", []string{"/"},
			dir("return", "301", "https://$host$request_uri"),
		),
	))
	return block("http", nil, children...)
}

// terminateServer renders one tls-termination server: ACME-managed TLS for the
// route's SNIs, reverse-proxying cleartext to the backend target. Render has
// already verified r.Backend is non-empty.
func terminateServer(r types.IngressRoute) *crossplane.Directive {
	serverName := append([]string{}, r.FQDNs...)
	return block("server", nil,
		dir("listen", strconv.Itoa(r.Listen), "ssl"),
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
// serving static files from the app's document root. A SPA falls any non-file
// path back to /index.html (client-side routing); a non-SPA returns 404. The
// index directive makes a directory request ("/") serve index.html. Render has
// already verified a.Root is non-empty.
func appServer(a types.IngressApp) *crossplane.Directive {
	fallback := []string{"$uri", "$uri/", "=404"}
	if a.Spa {
		fallback = []string{"$uri", "$uri/", "/index.html"}
	}
	return block("server", nil,
		dir("listen", "443", "ssl"),
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

// streamBlock renders the stream{} context: one raw L4 forward server per route,
// each prepending the PROXY protocol header so the backend learns the real client
// address (the buffered-backend use case that motivates the forward type).
func streamBlock(forward []types.IngressRoute) *crossplane.Directive {
	children := make(crossplane.Directives, 0, len(forward))
	for _, r := range forward {
		children = append(children, block("server", nil,
			dir("listen", strconv.Itoa(r.Listen)),
			dir("proxy_pass", fmt.Sprintf("%s:%d", r.Backend, r.Target)),
			dir("proxy_protocol", "on"),
		))
	}
	return block("stream", nil, children...)
}
