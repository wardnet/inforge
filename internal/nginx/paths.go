// Package nginx is the Hetzner-internal realization detail for the host ingress
// proxy: it renders a complete nginx.conf — the
// http servers that terminate ACME TLS and reverse-proxy to local services, the
// stream servers that L4-forward raw connections with the PROXY protocol, and the
// :80 server that answers ACME HTTP-01 challenges and redirects to HTTPS — plus
// the host install script. It is NOT a top-level inforge concept: the Hetzner
// compute provider uses it to realize a tls-termination spec over SSH, and another
// provider could realize the same spec with a managed load balancer instead.
//
// The rendering here is pure (routes in, string out) and deterministic (servers
// sorted by listen port then service); the transport that writes nginx.conf onto a
// host and reloads nginx lives in providers/hetzner.
package nginx

const (
	// ConfigPath is the single nginx config file this package owns end-to-end.
	// inforge renders the whole file (main + events + http + stream contexts)
	// rather than a conf.d drop-in, because load_module (main) and the stream
	// block cannot live inside the http{} include the stock nginx.conf provides.
	ConfigPath = "/etc/nginx/nginx.conf"

	// acmeModule is the dynamic ACME module shipped by the nginx.org repo
	// (package nginx-module-acme), loaded from nginx's modules directory.
	acmeModule = "modules/ngx_http_acme_module.so"

	// acmeIssuer is the logical name of the ACME issuer object every
	// tls-termination server references.
	acmeIssuer = "letsencrypt"

	// acmeDirectoryURL is Let's Encrypt's production ACMEv2 directory.
	acmeDirectoryURL = "https://acme-v02.api.letsencrypt.org/directory"

	// acmeStatePath holds the ACME account key and issued certificates between
	// reloads. The install script creates it; it lives under nginx's cache dir.
	acmeStatePath = "/var/cache/nginx/acme-letsencrypt"

	// resolverAddrs are the recursive resolvers nginx uses to look up the ACME
	// directory host. A public resolver is used rather than 127.0.0.1:53 because
	// stock cloud images run no local DNS server on :53.
	resolverAddrs = "1.1.1.1 8.8.8.8 valid=300s"

	// LoopbackBase is the first 127.0.0.1 port used by an internal TLS terminator
	// when a public listen port is shared by tls-termination/app servers AND a
	// forward (passthrough) service. In that "mixed" case the public socket moves
	// to a stream{} ssl_preread server that fans known SNIs to these loopback
	// terminators and the unknown SNI to the forward backend. One loopback port is
	// assigned per mixed public port, ascending from this base. Validation reserves
	// [LoopbackBase, LoopbackBase+MaxMixedPorts) on an ingress host so a co-located
	// backend target never collides with it.
	LoopbackBase = 11443

	// MaxMixedPorts bounds the reserved loopback range (far above any real ingress,
	// which has a handful of listen ports at most).
	MaxMixedPorts = 64
)
