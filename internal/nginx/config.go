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

// Render builds the complete nginx.conf for a host from its ingress routes. The
// output is deterministic: tls-termination servers and stream-forward servers are
// each sorted by (listen, service), so the same route set always renders the same
// bytes. http{} is emitted only when the host terminates TLS, stream{} only when
// it has a forward-with-target route; a host with neither yields a minimal config.
func Render(routes []types.IngressRoute) (string, error) {
	var terminate, forward []types.IngressRoute
	for _, r := range routes {
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

	var top crossplane.Directives
	top = append(top,
		dir("load_module", acmeModule),
		dir("user", "nginx"),
		dir("worker_processes", "auto"),
		dir("pid", "/run/nginx.pid"),
		block("events", nil, dir("worker_connections", "1024")),
	)

	if len(terminate) > 0 {
		top = append(top, httpBlock(terminate))
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
// per tls-termination route, and the :80 server that answers HTTP-01 challenges
// and redirects everything else to HTTPS.
func httpBlock(terminate []types.IngressRoute) *crossplane.Directive {
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

// backendAddr returns the address nginx proxies a route to: the route's resolved
// Backend (a private IP for a cross-host route, or "127.0.0.1" co-located). It
// defaults an empty Backend to loopback so a co-located route the program left
// implicit still renders to a valid upstream.
func backendAddr(r types.IngressRoute) string {
	if r.Backend == "" {
		return "127.0.0.1"
	}
	return r.Backend
}

// terminateServer renders one tls-termination server: ACME-managed TLS for the
// route's SNIs, reverse-proxying cleartext to the backend target.
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
			dir("proxy_pass", fmt.Sprintf("http://%s:%d", backendAddr(r), r.Target)),
			dir("proxy_set_header", "Host", "$host"),
			dir("proxy_set_header", "X-Forwarded-For", "$proxy_add_x_forwarded_for"),
			dir("proxy_set_header", "X-Forwarded-Proto", "$scheme"),
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
			dir("proxy_pass", fmt.Sprintf("%s:%d", backendAddr(r), r.Target)),
			dir("proxy_protocol", "on"),
		))
	}
	return block("stream", nil, children...)
}
