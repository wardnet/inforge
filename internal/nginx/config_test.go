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
	got, err := Render(routes, nil, nil, 0, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
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
	got, err := Render(routes, nil, nil, 0, nil)
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
	got, err := Render(routes, apps, nil, 0, nil)
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
	got, err := Render(routes, apps, nil, 0, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestRenderAppOnly: an ingress that serves only apps (no service routes) still
// gets the http block with the ACME issuer and the :80 challenge/redirect server,
// and no stream block — so its cert provisions for the app FQDN alone.
func TestRenderAppOnly(t *testing.T) {
	got, err := Render(nil, []types.IngressApp{
		{Name: "my", FQDN: "my.wardnet.network", Root: "/srv/wardnet/app/my/current", Spa: true},
	}, nil, 0, nil)
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
	}, nil, 0, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "include "+mimeTypesPath+";")
	// The fallback for an unmapped extension must be a byte stream, not text —
	// nginx's built-in text/plain default is what mislabels a JS bundle.
	assert.Contains(t, got, "default_type application/octet-stream;")
}

// TestRenderAppEmptyRootErrors: an app with no resolved document root fails loud
// rather than letting nginx serve the whole filesystem.
func TestRenderAppEmptyRootErrors(t *testing.T) {
	_, err := Render(nil, []types.IngressApp{{Name: "my", FQDN: "my.wardnet.network", Spa: true}}, nil, 0, nil)
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
	got, err := Render(routes, nil, health, 81, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "    server {\n        listen 81;\n        server_name api.svc.prd.use1.wardnet.network;\n        location = /healthz {\n            proxy_pass http://127.0.0.1:8081;\n            proxy_set_header Host $host;\n        }\n        location / {\n            return 404;\n        }\n    }")
	assert.Contains(t, got, "    server {\n        listen 81;\n        server_name web.svc.prd.use1.wardnet.network;\n        location = /healthz {\n            proxy_pass http://127.0.0.1:3001;\n            proxy_set_header Host $host;\n        }\n        location / {\n            return 404;\n        }\n    }")
	// Health servers are sorted by FQDN (api before web).
	assert.Less(t, strings.Index(got, "server_name api.svc.prd.use1.wardnet.network;\n        location ="),
		strings.Index(got, "server_name web.svc.prd.use1.wardnet.network;\n        location ="))
}

// TestRenderHealthOnly: an ingress with only health endpoints (no TLS, no apps)
// renders a minimal http block — no ACME issuer, no :80 redirect server, no stream.
func TestRenderHealthOnly(t *testing.T) {
	got, err := Render(nil, nil, []types.IngressHealth{
		{Service: "api", FQDN: "api.svc.prd.use1.wardnet.network", Target: 8081, Backend: "127.0.0.1", Paths: []string{"/healthz"}},
	}, 81, nil)
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
	}, 81, nil)
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
	}, 81, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowlist-only")
}

// TestRenderHealthNoBackendErrors: a health entry with no resolved backend fails
// loud, like a route.
func TestRenderHealthNoBackendErrors(t *testing.T) {
	_, err := Render(nil, nil, []types.IngressHealth{{Service: "api", FQDN: "api.svc", Target: 8081}}, 81, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no backend address")
}

// TestRenderHealthNoPortErrors: health entries with no public health port fail loud
// (the program must resolve the ingress's health port, default 81).
func TestRenderHealthNoPortErrors(t *testing.T) {
	_, err := Render(nil, nil, []types.IngressHealth{{Service: "api", FQDN: "api.svc", Target: 8081, Backend: "127.0.0.1", Paths: []string{"/healthz"}}}, 0, nil)
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
	ra, err := Render(a, nil, nil, 0, nil)
	require.NoError(t, err)
	rb, err := Render(b, nil, nil, 0, nil)
	require.NoError(t, err)
	assert.Equal(t, ra, rb)
}

// TestRenderForwardOnly: a host with only forward routes gets a stream block and
// no http/ACME block (nothing to terminate, so no :80 server).
func TestRenderForwardOnly(t *testing.T) {
	got, err := Render([]types.IngressRoute{
		{Service: "bridge", Type: types.IngressTypeForward, Listen: 443, Target: 8080, Backend: "127.0.0.1"},
	}, nil, nil, 0, nil)
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
	}, nil, nil, 0, nil)
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
	}, nil, nil, 0, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "proxy_pass http://10.0.1.5:8080;", "cross-host tls-termination proxies to the backend private IP")
	assert.Contains(t, got, "proxy_pass 10.0.1.6:5353;", "cross-host forward proxies to the backend private IP")
	assert.NotContains(t, got, "127.0.0.1", "no loopback when every route is cross-host")
}

// TestRenderUnknownTypeErrors guards the renderer against an unexpected route type.
func TestRenderUnknownTypeErrors(t *testing.T) {
	_, err := Render([]types.IngressRoute{{Service: "x", Type: "passthrough", Listen: 443, Backend: "127.0.0.1"}}, nil, nil, 0, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown type")
}

// TestRenderEmptyBackendErrors: a route with no resolved Backend fails loud rather
// than silently proxying the service to localhost (the program/provider must fill
// Backend — "127.0.0.1" co-located, the private IP cross-host).
func TestRenderEmptyBackendErrors(t *testing.T) {
	_, err := Render([]types.IngressRoute{{Service: "api", Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, FQDNs: []string{"api.svc"}}}, nil, nil, 0, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no backend address")
}

// TestRenderGateway: the north-south gateway server block — ACME on its single
// FQDN, one regex location per derived route (a listed service's public path
// glob) handing the request to the LOCAL mesh egress at the fixed gateway port
// (target named out-of-band in X-Mesh-Target so the path is preserved
// byte-for-byte), WebSocket-capable, XFF stamped at the edge, and a JSON 404
// default for undeclared paths (ADR-0034).
func TestRenderGateway(t *testing.T) {
	got, err := Render(nil, nil, nil, 0, []types.IngressGateway{{
		Name: "api",
		FQDN: "api.use1.wardnet.network",
		Routes: []types.IngressGatewayRoute{
			{Pattern: "/tunnel/**", Service: "tunneller"},
			{Pattern: "/v*/dns/**", Service: "ddns"},
			{Pattern: "/dns-status", Service: "ddns"},
		},
	}})
	require.NoError(t, err)
	assert.Contains(t, got, "server_name api.use1.wardnet.network;")
	assert.Contains(t, got, "acme_certificate letsencrypt;")
	assert.Contains(t, got, "listen 80;", "gateway alone still needs the ACME :80 server")
	assert.Contains(t, got, "map $http_upgrade $connection_upgrade", "WS upgrade map present")
	assert.Contains(t, got, `location ~ "^/v[^/]+/dns(/.*)?$" {`, "glob compiled to an anchored regex location")
	assert.Contains(t, got, `location ~ "^/dns-status$" {`, "exact glob compiled to an anchored regex location")
	assert.Contains(t, got, `location ~ "^/tunnel(/.*)?$" {`)
	assert.Contains(t, got, "proxy_set_header X-Mesh-Target ddns;")
	assert.Contains(t, got, "proxy_set_header X-Mesh-Target tunneller;")
	assert.Contains(t, got, fmt.Sprintf("proxy_pass http://127.0.0.1:%d;", meshpaths.GatewayEgressPort))
	// XFF is SET to the real client at the internet edge, not appended — a
	// daemon-supplied X-Forwarded-For must not survive into the mesh.
	assert.Contains(t, got, "proxy_set_header X-Forwarded-For $remote_addr;")
	assert.NotContains(t, got, "$proxy_add_x_forwarded_for", "the edge must not append a client-supplied XFF")
	assert.Contains(t, got, "proxy_set_header Upgrade $http_upgrade;")
	// Undeclared paths are answered with a JSON 404 at the edge, never proxied.
	assert.Contains(t, got, "default_type application/json;")
	assert.Contains(t, got, `return 404 '{"error":"not_found"}';`)
	// Routes render in sorted-pattern order (/dns-status before /tunnel/** before /v*/dns/**).
	assert.Less(t, strings.Index(got, `location ~ "^/dns-status$"`), strings.Index(got, `location ~ "^/tunnel(/.*)?$"`))
	assert.Less(t, strings.Index(got, `location ~ "^/tunnel(/.*)?$"`), strings.Index(got, `location ~ "^/v[^/]+/dns(/.*)?$"`))
	assert.NotContains(t, got, "stream {")
}

// TestRenderGatewaySelfHealth: the gateway's declared health probe paths are
// answered 200 "ok" by nginx itself on the 443 server — edge liveness over the
// real daemon TLS path — and render before the route locations.
func TestRenderGatewaySelfHealth(t *testing.T) {
	got, err := Render(nil, nil, nil, 0, []types.IngressGateway{{
		Name:             "api",
		FQDN:             "api.use1.wardnet.network",
		Routes:           []types.IngressGatewayRoute{{Pattern: "/tenants/**", Service: "tenants"}},
		HealthProbePaths: []string{"/healthz"},
	}})
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
	_, err := Render(nil, nil, nil, 0, []types.IngressGateway{{
		Name: "api", FQDN: "api.use1.wardnet.network",
		Routes: []types.IngressGatewayRoute{{Pattern: "/a/**/b", Service: "tenants"}},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid path glob")
}

// TestRenderGatewayMixedPort: a forward on :443 coexists with the gateway via
// ssl_preread — the gateway server moves to a loopback terminator and its FQDN
// joins the SNI map, exactly like an app.
func TestRenderGatewayMixedPort(t *testing.T) {
	got, err := Render([]types.IngressRoute{
		{Service: "tunneller", Type: types.IngressTypeForward, Listen: 443, Target: 9443, Backend: "127.0.0.1"},
	}, nil, nil, 0, []types.IngressGateway{{
		Name: "api", FQDN: "api.use1.wardnet.network",
		Routes: []types.IngressGatewayRoute{{Pattern: "/ddns/**", Service: "ddns"}},
	}})
	require.NoError(t, err)
	assert.Contains(t, got, "ssl_preread on;")
	assert.Contains(t, got, "api.use1.wardnet.network 127.0.0.1:11443;", "gateway FQDN routes to the loopback terminator")
	assert.Contains(t, got, "listen 127.0.0.1:11443 ssl proxy_protocol;", "gateway server moved to the loopback terminator")
}

// TestRenderGatewayNoFQDNErrors: a gateway without a resolved FQDN fails loud.
func TestRenderGatewayNoFQDNErrors(t *testing.T) {
	_, err := Render(nil, nil, nil, 0, []types.IngressGateway{{Name: "api"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no FQDN")
}
