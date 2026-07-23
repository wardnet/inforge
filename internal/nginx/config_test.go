package nginx

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/types"
)

// TestRenderGolden pins the full nginx.conf for the bridge shape (a multi-SNI
// tls-termination server sharing the http context with a stream-forward server on
// a DIFFERENT port — no collision, so no ssl_preread) byte-for-byte. This is the
// regression guarantee: wardnet has no rendered config to diff against yet, so the
// renderer guards itself.
func TestRenderGolden(t *testing.T) {
	// Declared out of order to prove the renderer sorts deterministically.
	routes := []types.IngressRoute{
		{Service: "bridge", Type: types.IngressTypeForward, Listen: 853, Target: 5353, Backend: "127.0.0.1"},
		{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Backend: "127.0.0.1",
			FQDNs: []string{"api.svc.prd.use1.wardnet.network", "key-broker.wardnet.network"}},
	}

	const want = `# Managed by inforge — do not edit by hand.
load_module modules/ngx_http_acme_module.so;
user nginx;
worker_processes auto;
pid /run/nginx.pid;
events {
    worker_connections 1024;
}
http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    resolver 1.1.1.1 8.8.8.8 valid=300s;
    acme_issuer letsencrypt {
        uri https://acme-v02.api.letsencrypt.org/directory;
        state_path /var/cache/nginx/acme-letsencrypt;
        accept_terms_of_service;
    }
    acme_shared_zone zone=ngx_acme_shared:1M;
    server {
        listen 443 ssl;
        server_name api.svc.prd.use1.wardnet.network key-broker.wardnet.network;
        acme_certificate letsencrypt;
        ssl_certificate $acme_certificate;
        ssl_certificate_key $acme_certificate_key;
        ssl_certificate_cache max=2;
        location / {
            proxy_pass http://127.0.0.1:8080;
            proxy_set_header Host $host;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
        }
    }
    server {
        listen 80;
        location / {
            return 301 https://$host$request_uri;
        }
    }
}
stream {
    server {
        listen 853;
        proxy_pass 127.0.0.1:5353;
        proxy_protocol on;
    }
}
`
	got, err := Render(routes, nil, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestRenderRateLimit checks the ADR-0043 IP rate-limit rendering: one uniform limit
// stamped on a tls-termination route (http limit_req + limit_conn) and a forward route
// (stream limit_conn), with each shared-memory zone declared exactly once per context
// and throttled requests answered 429 rather than nginx's default 503.
func TestRenderRateLimit(t *testing.T) {
	rl := &types.RateLimitProfile{Name: "edge", RPS: 20, Burst: 40, MaxConn: 40}
	routes := []types.IngressRoute{
		{Service: "dns", Type: types.IngressTypeForward, Listen: 853, Target: 5353, Backend: "127.0.0.1", RateLimit: rl},
		{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Backend: "127.0.0.1",
			FQDNs: []string{"api.svc.prd.use1.wardnet.network"}, RateLimit: rl},
	}
	got, err := Render(routes, nil, nil, 0, nil, nil)
	require.NoError(t, err)

	// http{} zones, declared once, with the 429 status overrides.
	assert.Contains(t, got, "limit_req_zone $binary_remote_addr zone=rl_edge:10m rate=20r/s;")
	assert.Contains(t, got, "limit_conn_zone $binary_remote_addr zone=rlc_edge:10m;")
	assert.Contains(t, got, "limit_req_status 429;")
	assert.Contains(t, got, "limit_conn_status 429;")
	assert.Equal(t, 1, strings.Count(got, "limit_req_zone $binary_remote_addr zone=rl_edge"), "req zone declared once")

	// per-location http limits on the tls-termination server.
	assert.Contains(t, got, "limit_req zone=rl_edge burst=40 nodelay;")
	assert.Contains(t, got, "limit_conn rlc_edge 40;")

	// stream (forward) connection limit — its own zone namespace.
	assert.Contains(t, got, "limit_conn_zone $binary_remote_addr zone=rls_edge:10m;")
	assert.Contains(t, got, "limit_conn rls_edge 40;")
}

// TestRenderRateLimitExemptsHealthAndACME confirms the limit lands only on the
// service-facing locations: the public health servers and the :80 ACME/redirect server
// are never rate-limited (they are rendered by separate functions that carry no profile).
func TestRenderRateLimitExemptsHealthAndACME(t *testing.T) {
	rl := &types.RateLimitProfile{Name: "edge", RPS: 20, Burst: 40, MaxConn: 40}
	routes := []types.IngressRoute{
		{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Backend: "127.0.0.1",
			FQDNs: []string{"api.svc.prd.use1.wardnet.network"}, RateLimit: rl},
	}
	health := []types.IngressHealth{
		{Service: "api", FQDN: "api.svc.prd.use1.wardnet.network", Target: 9000, Backend: "127.0.0.1", Paths: []string{"/healthz"}},
	}
	got, err := Render(routes, nil, health, 81, nil, nil)
	require.NoError(t, err)
	// Exactly one limited location — the tls-termination route. Health + ACME are exempt.
	assert.Equal(t, 1, strings.Count(got, "limit_req zone=rl_edge"))
	assert.Contains(t, got, "listen 81;") // the health server exists...
}

// TestRenderNoRateLimit guards the golden's premise: a nil profile emits no limit
// directives at all, so an env without rate limiting renders byte-identically to before.
func TestRenderNoRateLimit(t *testing.T) {
	routes := []types.IngressRoute{
		{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Backend: "127.0.0.1",
			FQDNs: []string{"api.svc.prd.use1.wardnet.network"}},
	}
	got, err := Render(routes, nil, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.NotContains(t, got, "limit_req")
	assert.NotContains(t, got, "limit_conn")
}

// TestRenderMixedPreread pins the mixed-port shape: two tls-termination services
// and one forward (passthrough) sharing listen 443. The public :443 moves into a
// stream{} ssl_preread server that maps known SNIs to an internal loopback
// terminator and the unknown SNI to the forward backend; the http terminators move
// to 127.0.0.1:<loopback> ssl proxy_protocol and the real client address is
// recovered via set_real_ip_from + real_ip_header.
func TestRenderMixedPreread(t *testing.T) {
	routes := []types.IngressRoute{
		{Service: "raw", Type: types.IngressTypeForward, Listen: 443, Target: 9000, Backend: "127.0.0.1"},
		{Service: "web", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 3000, Backend: "127.0.0.1",
			FQDNs: []string{"web.svc.prd.use1.wardnet.network"}},
		{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Backend: "127.0.0.1",
			FQDNs: []string{"api.svc.prd.use1.wardnet.network"}},
	}

	const want = `# Managed by inforge — do not edit by hand.
load_module modules/ngx_http_acme_module.so;
user nginx;
worker_processes auto;
pid /run/nginx.pid;
events {
    worker_connections 1024;
}
http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    resolver 1.1.1.1 8.8.8.8 valid=300s;
    acme_issuer letsencrypt {
        uri https://acme-v02.api.letsencrypt.org/directory;
        state_path /var/cache/nginx/acme-letsencrypt;
        accept_terms_of_service;
    }
    acme_shared_zone zone=ngx_acme_shared:1M;
    set_real_ip_from 127.0.0.1;
    real_ip_header proxy_protocol;
    server {
        listen 127.0.0.1:11443 ssl proxy_protocol;
        server_name api.svc.prd.use1.wardnet.network;
        acme_certificate letsencrypt;
        ssl_certificate $acme_certificate;
        ssl_certificate_key $acme_certificate_key;
        ssl_certificate_cache max=2;
        location / {
            proxy_pass http://127.0.0.1:8080;
            proxy_set_header Host $host;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
        }
    }
    server {
        listen 127.0.0.1:11443 ssl proxy_protocol;
        server_name web.svc.prd.use1.wardnet.network;
        acme_certificate letsencrypt;
        ssl_certificate $acme_certificate;
        ssl_certificate_key $acme_certificate_key;
        ssl_certificate_cache max=2;
        location / {
            proxy_pass http://127.0.0.1:3000;
            proxy_set_header Host $host;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
        }
    }
    server {
        listen 80;
        location / {
            return 301 https://$host$request_uri;
        }
    }
}
stream {
    map $ssl_preread_server_name $ingress_upstream_443 {
        api.svc.prd.use1.wardnet.network 127.0.0.1:11443;
        web.svc.prd.use1.wardnet.network 127.0.0.1:11443;
        default 127.0.0.1:9000;
    }
    server {
        listen 443;
        ssl_preread on;
        proxy_pass $ingress_upstream_443;
        proxy_protocol on;
    }
}
`
	got, err := Render(routes, nil, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestRenderMixedWithApp: a forward on 443 alongside an app (apps always serve on
// 443) makes 443 mixed, so the app server moves to the loopback terminator and its
// FQDN joins the ssl_preread map.
func TestRenderMixedWithApp(t *testing.T) {
	routes := []types.IngressRoute{
		{Service: "raw", Type: types.IngressTypeForward, Listen: 443, Target: 9000, Backend: "127.0.0.1"},
	}
	apps := []types.IngressApp{
		{Name: "dashboard", FQDN: "my.use1.wardnet.network", Root: "/srv/wardnet/app/dashboard/current", Spa: true},
	}
	got, err := Render(routes, apps, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "listen 127.0.0.1:11443 ssl proxy_protocol;", "app server moves to the loopback terminator")
	assert.Contains(t, got, "my.use1.wardnet.network 127.0.0.1:11443;", "app FQDN joins the preread map")
	assert.Contains(t, got, "default 127.0.0.1:9000;", "forward backend is the map default")
	assert.Contains(t, got, "ssl_preread on;")
}

// TestRenderAppGolden pins the full nginx.conf for an app-serving ingress that
// also fronts a service: a SPA app and a non-SPA app share the http context with a
// tls-termination service server and the :80 ACME/redirect server. App servers
// follow route servers and are sorted by FQDN.
func TestRenderAppGolden(t *testing.T) {
	routes := []types.IngressRoute{
		{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Backend: "127.0.0.1",
			FQDNs: []string{"api.svc.prd.use1.wardnet.network"}},
	}
	// Declared out of FQDN order to prove the renderer sorts apps deterministically.
	apps := []types.IngressApp{
		{Name: "marketing", FQDN: "www.use1.wardnet.network", Root: "/srv/wardnet/app/marketing/current", Spa: false},
		{Name: "dashboard", FQDN: "my.use1.wardnet.network", Root: "/srv/wardnet/app/dashboard/current", Spa: true},
	}

	const want = `# Managed by inforge — do not edit by hand.
load_module modules/ngx_http_acme_module.so;
user nginx;
worker_processes auto;
pid /run/nginx.pid;
events {
    worker_connections 1024;
}
http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    resolver 1.1.1.1 8.8.8.8 valid=300s;
    acme_issuer letsencrypt {
        uri https://acme-v02.api.letsencrypt.org/directory;
        state_path /var/cache/nginx/acme-letsencrypt;
        accept_terms_of_service;
    }
    acme_shared_zone zone=ngx_acme_shared:1M;
    server {
        listen 443 ssl;
        server_name api.svc.prd.use1.wardnet.network;
        acme_certificate letsencrypt;
        ssl_certificate $acme_certificate;
        ssl_certificate_key $acme_certificate_key;
        ssl_certificate_cache max=2;
        location / {
            proxy_pass http://127.0.0.1:8080;
            proxy_set_header Host $host;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
        }
    }
    server {
        listen 443 ssl;
        server_name my.use1.wardnet.network;
        acme_certificate letsencrypt;
        ssl_certificate $acme_certificate;
        ssl_certificate_key $acme_certificate_key;
        ssl_certificate_cache max=2;
        root /srv/wardnet/app/dashboard/current;
        index index.html;
        location / {
            try_files $uri $uri/ /index.html;
        }
    }
    server {
        listen 443 ssl;
        server_name www.use1.wardnet.network;
        acme_certificate letsencrypt;
        ssl_certificate $acme_certificate;
        ssl_certificate_key $acme_certificate_key;
        ssl_certificate_cache max=2;
        root /srv/wardnet/app/marketing/current;
        index index.html;
        location / {
            try_files $uri $uri/ =404;
        }
    }
    server {
        listen 80;
        location / {
            return 301 https://$host$request_uri;
        }
    }
}
`
	got, err := Render(routes, apps, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestRenderAppOnly: an ingress that serves only apps (no service routes) still
// gets the http block with the ACME issuer and the :80 challenge/redirect server,
// and no stream block — so its cert provisions for the app FQDN alone.
func TestRenderAppOnly(t *testing.T) {
	got, err := Render(nil, []types.IngressApp{
		{Name: "my", FQDN: "my.wardnet.network", Root: "/srv/wardnet/app/my/current", Spa: true},
	}, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "http {")
	assert.Contains(t, got, "acme_certificate letsencrypt;")
	assert.Contains(t, got, "server_name my.wardnet.network;")
	assert.Contains(t, got, "root /srv/wardnet/app/my/current;")
	assert.Contains(t, got, "try_files $uri $uri/ /index.html;")
	assert.Contains(t, got, "listen 80;")
	assert.NotContains(t, got, "stream {")
}

// TestRenderIncludesMimeTypes guards the Content-Type of every static byte an
// app serves. We render nginx.conf whole (no stock conf.d include), so nothing
// pulls in mime.types for us — and nginx's built-in default_type is text/plain.
// Without the include, an app's index-*.js came back as text/plain and the
// browser refused it outright ("Expected a JavaScript-or-Wasm module script but
// the server responded with a MIME type of text/plain"), which breaks every
// React app we serve, since strict MIME checking applies to ES modules.
func TestRenderIncludesMimeTypes(t *testing.T) {
	got, err := Render(nil, []types.IngressApp{
		{Name: "my", FQDN: "my.wardnet.network", Root: "/srv/wardnet/app/my/current", Spa: true},
	}, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "include "+mimeTypesPath+";")
	// The fallback for an unmapped extension must be a byte stream, not text —
	// nginx's built-in text/plain default is what mislabels a JS bundle.
	assert.Contains(t, got, "default_type application/octet-stream;")
}

// TestRenderAppEmptyRootErrors: an app with no resolved document root fails loud
// rather than letting nginx serve the whole filesystem.
func TestRenderAppEmptyRootErrors(t *testing.T) {
	_, err := Render(nil, []types.IngressApp{{Name: "my", FQDN: "my.wardnet.network", Spa: true}}, nil, 0, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no document root")
}

// TestRenderHealthGolden pins the health tier: two services declaring a backend
// health port are surfaced as plain-HTTP servers on the ingress's public health
// port (81 here), demuxed strictly by server_name (the service FQDN) and proxied to
// each backend's health port. Health shares the http context with the :80 ACME
// server only when something also terminates TLS.
func TestRenderHealthGolden(t *testing.T) {
	routes := []types.IngressRoute{
		{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Backend: "127.0.0.1",
			FQDNs: []string{"api.svc.prd.use1.wardnet.network"}},
	}
	health := []types.IngressHealth{
		{Service: "web", FQDN: "web.svc.prd.use1.wardnet.network", Target: 3001, Backend: "127.0.0.1", Paths: []string{"/healthz"}},
		{Service: "api", FQDN: "api.svc.prd.use1.wardnet.network", Target: 8081, Backend: "127.0.0.1", Paths: []string{"/healthz"}},
	}
	got, err := Render(routes, nil, health, 81, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "    server {\n        listen 81;\n        server_name api.svc.prd.use1.wardnet.network;\n        location = /healthz {\n            proxy_pass http://127.0.0.1:8081;\n            proxy_set_header Host $host;\n        }\n        location / {\n            return 404;\n        }\n    }")
	assert.Contains(t, got, "    server {\n        listen 81;\n        server_name web.svc.prd.use1.wardnet.network;\n        location = /healthz {\n            proxy_pass http://127.0.0.1:3001;\n            proxy_set_header Host $host;\n        }\n        location / {\n            return 404;\n        }\n    }")
	// Health servers are sorted by FQDN (api before web).
	assert.Less(t, strings.Index(got, "server_name api.svc.prd.use1.wardnet.network;\n        location ="),
		strings.Index(got, "server_name web.svc.prd.use1.wardnet.network;\n        location ="))
}

// TestRenderHealthCatchAll: the health port carries an explicit default_server
// that 404s. Without it nginx would promote the first health server to the
// implicit default and proxy an unknown/absent Host to that service's backend.
func TestRenderHealthCatchAll(t *testing.T) {
	got, err := Render(nil, nil, []types.IngressHealth{
		{Service: "web", FQDN: "web.svc.prd.use1.wardnet.network", Target: 3001, Backend: "127.0.0.1", Paths: []string{"/healthz"}},
		{Service: "api", FQDN: "api.svc.prd.use1.wardnet.network", Target: 8081, Backend: "127.0.0.1", Paths: []string{"/healthz"}},
	}, 81, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "    server {\n        listen 81 default_server;\n        server_name _;\n        return 404;\n    }")
	// The catch-all precedes every named health server, so no service's server can
	// be the implicit default for the port.
	assert.Less(t, strings.Index(got, "listen 81 default_server;"), strings.Index(got, "server_name api.svc.prd.use1.wardnet.network;"))
	// It is emitted exactly once for the port, whatever the health count.
	assert.Equal(t, 1, strings.Count(got, "default_server"))
}

// TestRenderNoHealthNoCatchAll: with no health endpoints there is no health port
// to defend, so no catch-all server is emitted.
func TestRenderNoHealthNoCatchAll(t *testing.T) {
	got, err := Render([]types.IngressRoute{
		{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Backend: "127.0.0.1", FQDNs: []string{"api.example.com"}},
	}, nil, nil, 81, nil, nil)
	require.NoError(t, err)
	assert.NotContains(t, got, "default_server")
}

// TestRenderForwardOn80TerminatingHostRejected: :80 belongs to nginx's ACME
// HTTP-01 challenge/redirect server whenever the host terminates TLS — for ANY
// reason (a tls-termination route, an app, or the gateway). A forward there is a
// stream{} server on the same public socket, so nginx would refuse to start;
// Render must fail loud rather than emit a config that cannot come up. Validation
// rejects this first — this is the enforcement layer behind a render that skipped it.
func TestRenderForwardOn80TerminatingHostRejected(t *testing.T) {
	fwd80 := []types.IngressRoute{
		{Service: "relay", Type: types.IngressTypeForward, Listen: 80, Target: 8080, Backend: "127.0.0.1"},
	}
	// Terminating because of an app.
	_, err := Render(fwd80, []types.IngressApp{{Name: "dash", FQDN: "my.example.com", Root: "/srv/dash", Spa: true}}, nil, 81, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ACME HTTP-01")
	// Terminating because of a gateway termination server.
	_, err = Render(fwd80, nil, nil, 81, []types.IngressGateway{{Name: "api", FQDN: "api.example.com", Backend: "127.0.0.1", HTTPPort: GatewayHTTPPort}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ACME HTTP-01")
	// Terminating because of a tls-termination route.
	_, err = Render(append(append([]types.IngressRoute(nil), fwd80...),
		types.IngressRoute{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Backend: "127.0.0.1", FQDNs: []string{"api.example.com"}},
	), nil, nil, 81, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ACME HTTP-01")
	// Nothing terminates TLS: no :80 http server, so the forward owns the port.
	got, err := Render(fwd80, nil, nil, 81, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "listen 80;")
}

// TestRenderForwardOnHealthPortRejected: the public health port is nginx's too —
// a forward on it would put the same socket in stream{} and http{}.
func TestRenderForwardOnHealthPortRejected(t *testing.T) {
	_, err := Render([]types.IngressRoute{
		{Service: "relay", Type: types.IngressTypeForward, Listen: 81, Target: 8080, Backend: "127.0.0.1"},
	}, nil, []types.IngressHealth{
		{Service: "api", FQDN: "api.svc.prd.use1.wardnet.network", Target: 8081, Backend: "127.0.0.1", Paths: []string{"/healthz"}},
	}, 81, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public health port")
}

// TestRenderHealthOnly: an ingress with only health endpoints (no TLS, no apps)
// renders a minimal http block — no ACME issuer, no :80 redirect server, no stream.
func TestRenderHealthOnly(t *testing.T) {
	got, err := Render(nil, nil, []types.IngressHealth{
		{Service: "api", FQDN: "api.svc.prd.use1.wardnet.network", Target: 8081, Backend: "127.0.0.1", Paths: []string{"/healthz"}},
	}, 81, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "http {")
	assert.Contains(t, got, "listen 81;")
	assert.Contains(t, got, "proxy_pass http://127.0.0.1:8081;")
	assert.NotContains(t, got, "acme_issuer")
	assert.NotContains(t, got, "listen 80;")
	assert.NotContains(t, got, "stream {")
}

// TestRenderHealthPaths: a health entry with declared probe paths renders one
// exact-match location per path plus a 404 catch-all — the listener is
// allowlist-only (ADR-0034). Paths render sorted.
func TestRenderHealthPaths(t *testing.T) {
	got, err := Render(nil, nil, []types.IngressHealth{
		{Service: "api", FQDN: "api.svc.prd.use1.wardnet.network", Target: 8081, Backend: "127.0.0.1",
			Paths: []string{"/readyz", "/healthz"}},
	}, 81, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "        location = /healthz {\n            proxy_pass http://127.0.0.1:8081;\n            proxy_set_header Host $host;\n        }")
	assert.Contains(t, got, "        location = /readyz {\n            proxy_pass http://127.0.0.1:8081;\n            proxy_set_header Host $host;\n        }")
	assert.Contains(t, got, "        location / {\n            return 404;\n        }")
	assert.Less(t, strings.Index(got, "location = /healthz"), strings.Index(got, "location = /readyz"))
	assert.NotContains(t, got, "location / {\n            proxy_pass", "no full-open location when paths are declared")
}

// TestRenderHealthNoPathsErrors: a health entry with no declared probe paths
// fails the render loud — the listener is allowlist-only, never full-open
// (ADR-0034); a pre-allowlist manifest deployed without validation must not
// silently proxy the whole backend port.
func TestRenderHealthNoPathsErrors(t *testing.T) {
	_, err := Render(nil, nil, []types.IngressHealth{
		{Service: "api", FQDN: "api.svc", Target: 8081, Backend: "127.0.0.1"},
	}, 81, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowlist-only")
}

// TestRenderHealthNoBackendErrors: a health entry with no resolved backend fails
// loud, like a route.
func TestRenderHealthNoBackendErrors(t *testing.T) {
	_, err := Render(nil, nil, []types.IngressHealth{{Service: "api", FQDN: "api.svc", Target: 8081}}, 81, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no backend address")
}

// TestRenderHealthNoPortErrors: health entries with no public health port fail loud
// (the program must resolve the ingress's health port, default 81).
func TestRenderHealthNoPortErrors(t *testing.T) {
	_, err := Render(nil, nil, []types.IngressHealth{{Service: "api", FQDN: "api.svc", Target: 8081, Backend: "127.0.0.1", Paths: []string{"/healthz"}}}, 0, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no public health port")
}

// TestRenderDeterministic: the same route set, in any input order, renders the
// same bytes.
func TestRenderDeterministic(t *testing.T) {
	a := []types.IngressRoute{
		{Service: "a", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 1000, FQDNs: []string{"a.svc"}, Backend: "127.0.0.1"},
		{Service: "b", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 2000, FQDNs: []string{"b.svc"}, Backend: "127.0.0.1"},
		{Service: "c", Type: types.IngressTypeForward, Listen: 853, Target: 3000, Backend: "127.0.0.1"},
	}
	b := []types.IngressRoute{a[2], a[1], a[0]}
	ra, err := Render(a, nil, nil, 0, nil, nil)
	require.NoError(t, err)
	rb, err := Render(b, nil, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, ra, rb)
}

// TestRenderForwardOnly: a host with only forward routes gets a stream block and
// no http/ACME block (nothing to terminate, so no :80 server).
func TestRenderForwardOnly(t *testing.T) {
	got, err := Render([]types.IngressRoute{
		{Service: "bridge", Type: types.IngressTypeForward, Listen: 443, Target: 8080, Backend: "127.0.0.1"},
	}, nil, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "stream {")
	assert.Contains(t, got, "proxy_protocol on;")
	assert.NotContains(t, got, "http {")
	assert.NotContains(t, got, "acme_issuer")
	assert.NotContains(t, got, "listen 80;")
	assert.NotContains(t, got, "ssl_preread")
}

// TestRenderTerminateOnly: a host with only tls-termination routes gets http (with
// the ACME issuer and the :80 challenge/redirect server) and no stream block.
func TestRenderTerminateOnly(t *testing.T) {
	got, err := Render([]types.IngressRoute{
		{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, FQDNs: []string{"api.svc"}, Backend: "127.0.0.1"},
	}, nil, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "http {")
	assert.Contains(t, got, "acme_certificate letsencrypt;")
	assert.Contains(t, got, "listen 80;")
	assert.Contains(t, got, "return 301 https://$host$request_uri;")
	assert.NotContains(t, got, "stream {")
	// The :80 server is the single redirect server (the module intercepts the
	// challenge path before location matching).
	assert.Equal(t, 1, strings.Count(got, "listen 80;"))
}

// TestRenderCrossHostBackend: a route whose Backend is a private IP (cross-host)
// renders its proxy_pass to that address; an empty Backend defaults to loopback.
func TestRenderCrossHostBackend(t *testing.T) {
	got, err := Render([]types.IngressRoute{
		{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, FQDNs: []string{"api.svc"}, Backend: "10.0.1.5"},
		{Service: "dns", Type: types.IngressTypeForward, Listen: 853, Target: 5353, Backend: "10.0.1.6"},
	}, nil, nil, 0, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "proxy_pass http://10.0.1.5:8080;", "cross-host tls-termination proxies to the backend private IP")
	assert.Contains(t, got, "proxy_pass 10.0.1.6:5353;", "cross-host forward proxies to the backend private IP")
	assert.NotContains(t, got, "127.0.0.1", "no loopback when every route is cross-host")
}

// TestRenderUnknownTypeErrors guards the renderer against an unexpected route type.
func TestRenderUnknownTypeErrors(t *testing.T) {
	_, err := Render([]types.IngressRoute{{Service: "x", Type: "passthrough", Listen: 443, Backend: "127.0.0.1"}}, nil, nil, 0, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown type")
}

// TestRenderEmptyBackendErrors: a route with no resolved Backend fails loud rather
// than silently proxying the service to localhost (the program/provider must fill
// Backend — "127.0.0.1" co-located, the private IP cross-host).
func TestRenderEmptyBackendErrors(t *testing.T) {
	_, err := Render([]types.IngressRoute{{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, FQDNs: []string{"api.svc"}}}, nil, nil, 0, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no backend address")
}

// coLocatedGateway builds the termination + routing halves of a co-located
// private gateway (ADR-0045) for the render tests: the ingress-side TLS termination
// (proxy to the gateway's loopback HTTP port) and the gateway-side plain-HTTP routing
// server (real_ip recovery → mesh egress).
func coLocatedGateway(name, fqdn string, routes []types.IngressGatewayRoute, healthPaths []string) (term, route types.IngressGateway) {
	term = types.IngressGateway{Name: name, FQDN: fqdn, Backend: "127.0.0.1", HTTPPort: GatewayHTTPPort}
	route = types.IngressGateway{
		Name: name, FQDN: fqdn, Routes: routes, HealthProbePaths: healthPaths,
		HTTPPort: GatewayHTTPPort, ListenAddr: "127.0.0.1", RealIPFrom: "127.0.0.1",
	}
	return term, route
}

// TestRenderGateway: a private gateway (ADR-0045) renders two servers. The ingress
// TERMINATION server holds ACME on the FQDN and reverse-proxies the whole host to the
// gateway's plain-HTTP port, appending the real client to XFF. The gateway ROUTING
// server recovers the client IP from that XFF (trusting only the ingress), then hands
// each derived route to the LOCAL mesh egress (target out-of-band in X-Mesh-Target so
// the path is preserved), WebSocket-capable, with a JSON 404 default (ADR-0034).
func TestRenderGateway(t *testing.T) {
	term, route := coLocatedGateway("api", "api.use1.wardnet.network", []types.IngressGatewayRoute{
		{Pattern: "/tunnel/**", Service: "tunneller"},
		{Pattern: "/v*/dns/**", Service: "ddns"},
		{Pattern: "/dns-status", Service: "ddns"},
	}, nil)
	got, err := Render(nil, nil, nil, 0, []types.IngressGateway{term}, []types.IngressGateway{route})
	require.NoError(t, err)
	// Termination server: ACME on the FQDN, blind-proxy to the gateway HTTP port,
	// APPEND the real client to XFF (the trusted internet edge).
	assert.Contains(t, got, "server_name api.use1.wardnet.network;")
	assert.Contains(t, got, "acme_certificate letsencrypt;")
	assert.Contains(t, got, "listen 80;", "gateway alone still needs the ACME :80 server")
	assert.Contains(t, got, fmt.Sprintf("proxy_pass http://127.0.0.1:%d;", GatewayHTTPPort), "termination blind-proxies to the gateway HTTP port")
	assert.Contains(t, got, "proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;", "the ingress edge appends the real client")
	// Routing server: recover the client IP from the ingress XFF (trust only it),
	// then hand each route to the mesh egress with X-Mesh-Target.
	assert.Contains(t, got, fmt.Sprintf("listen 127.0.0.1:%d;", GatewayHTTPPort), "routing server listens plain HTTP on loopback")
	assert.Contains(t, got, "set_real_ip_from 127.0.0.1;")
	assert.Contains(t, got, "real_ip_header X-Forwarded-For;")
	assert.Contains(t, got, "real_ip_recursive on;")
	assert.Contains(t, got, "map $http_upgrade $connection_upgrade", "WS upgrade map present")
	assert.Contains(t, got, `location ~ "^/v[^/]+/dns(/.*)?$" {`, "glob compiled to an anchored regex location")
	assert.Contains(t, got, `location ~ "^/dns-status$" {`, "exact glob compiled to an anchored regex location")
	assert.Contains(t, got, `location ~ "^/tunnel(/.*)?$" {`)
	assert.Contains(t, got, "proxy_set_header X-Mesh-Target ddns;")
	assert.Contains(t, got, "proxy_set_header X-Mesh-Target tunneller;")
	assert.Contains(t, got, fmt.Sprintf("proxy_pass http://127.0.0.1:%d;", meshpaths.GatewayEgressPort))
	// Toward the mesh, XFF is SET to the recovered client ($remote_addr) — the callee
	// reads it as the leftmost entry.
	assert.Contains(t, got, "proxy_set_header X-Forwarded-For $remote_addr;")
	assert.Contains(t, got, "proxy_set_header Upgrade $http_upgrade;")
	// Undeclared paths are answered with a JSON 404, never proxied.
	assert.Contains(t, got, "default_type application/json;")
	assert.Contains(t, got, `return 404 '{"error":"not_found"}';`)
	// Routes render in sorted-pattern order (/dns-status before /tunnel/** before /v*/dns/**).
	assert.Less(t, strings.Index(got, `location ~ "^/dns-status$"`), strings.Index(got, `location ~ "^/tunnel(/.*)?$"`))
	assert.Less(t, strings.Index(got, `location ~ "^/tunnel(/.*)?$"`), strings.Index(got, `location ~ "^/v[^/]+/dns(/.*)?$"`))
	assert.NotContains(t, got, "stream {")
}

// TestRenderGatewaySelfHealth: the gateway's declared health probe paths are
// answered 200 "ok" by nginx itself on the ROUTING server (reached through the
// ingress over the real public path) and render before the route locations.
func TestRenderGatewaySelfHealth(t *testing.T) {
	term, route := coLocatedGateway("api", "api.use1.wardnet.network",
		[]types.IngressGatewayRoute{{Pattern: "/tenants/**", Service: "tenants"}}, []string{"/healthz"})
	got, err := Render(nil, nil, nil, 0, []types.IngressGateway{term}, []types.IngressGateway{route})
	require.NoError(t, err)
	assert.Contains(t, got, "location = /healthz {")
	assert.Contains(t, got, "default_type text/plain;")
	assert.Contains(t, got, "return 200 ok;")
	assert.Less(t, strings.Index(got, "location = /healthz"), strings.Index(got, `location ~ "^/tenants(/.*)?$"`))
}

// TestRenderGatewayBadGlobErrors: a route pattern that fails pathglob.Parse
// fails the render loud (validation guarantees it never happens; the renderer
// must not emit a broken location if it does).
func TestRenderGatewayBadGlobErrors(t *testing.T) {
	_, route := coLocatedGateway("api", "api.use1.wardnet.network",
		[]types.IngressGatewayRoute{{Pattern: "/a/**/b", Service: "tenants"}}, nil)
	_, err := Render(nil, nil, nil, 0, nil, []types.IngressGateway{route})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid path glob")
}

// TestRenderGatewayMixedPort: a forward on :443 coexists with the gateway
// TERMINATION server via ssl_preread — the termination moves to a loopback
// terminator and its FQDN joins the SNI map, exactly like an app. The routing
// server (plain HTTP on its own port) is unaffected.
func TestRenderGatewayMixedPort(t *testing.T) {
	term, route := coLocatedGateway("api", "api.use1.wardnet.network",
		[]types.IngressGatewayRoute{{Pattern: "/ddns/**", Service: "ddns"}}, nil)
	got, err := Render([]types.IngressRoute{
		{Service: "tunneller", Type: types.IngressTypeForward, Listen: 443, Target: 9443, Backend: "127.0.0.1"},
	}, nil, nil, 0, []types.IngressGateway{term}, []types.IngressGateway{route})
	require.NoError(t, err)
	assert.Contains(t, got, "ssl_preread on;")
	assert.Contains(t, got, "api.use1.wardnet.network 127.0.0.1:11443;", "gateway FQDN routes to the loopback terminator")
	assert.Contains(t, got, "listen 127.0.0.1:11443 ssl proxy_protocol;", "gateway termination moved to the loopback terminator")
}

// TestRenderGatewayNoFQDNErrors: a gateway termination without a resolved FQDN fails loud.
func TestRenderGatewayNoFQDNErrors(t *testing.T) {
	_, err := Render(nil, nil, nil, 0, []types.IngressGateway{{Name: "api", Backend: "127.0.0.1", HTTPPort: GatewayHTTPPort}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no FQDN")
}

// TestRenderGatewayUnresolvedBackendErrors: a termination missing its resolved
// backend (the private gateway address) fails loud rather than proxying to nowhere.
func TestRenderGatewayUnresolvedBackendErrors(t *testing.T) {
	_, err := Render(nil, nil, nil, 0, []types.IngressGateway{{Name: "api", FQDN: "api.svc"}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved private gateway address")
}
