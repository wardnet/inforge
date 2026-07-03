// Package meshnginx renders the per-host east-west mesh proxy config (ADR-0032) —
// a SECOND nginx, separate from the north-south one (internal/nginx), so the mesh
// (which holds the co-located services' leaf keys and makes the east-west authz
// decisions) never shares a process with the internet-facing tier. It is pure
// (Pulumi-free, like internal/nginx): the program derives the inputs and the
// provider writes the output.
//
// This file renders the callee (ingress) plane: one mTLS server per co-located mesh
// service, selected by SNI (the leaf's <service>.<scope>.mesh DNS SAN), verifying
// the peer's client cert against the mesh trust bundle, enforcing the service's
// allow-list on the peer identity (the cert CN, <scope>/<service>), and forwarding
// to the service's loopback port with the identity injected as X-Service-Identity.
// The caller (egress) plane and the global mesh gateway are rendered separately.
package meshnginx

import (
	"fmt"
	"sort"
	"strings"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
	"github.com/wardnet/inforge/internal/meshpaths"
)

// dir constructs a simple (block-less) nginx directive.
func dir(name string, args ...string) *crossplane.Directive {
	return &crossplane.Directive{Directive: name, Args: args}
}

// block constructs a block directive (rendered with braces).
func block(name string, args []string, children ...*crossplane.Directive) *crossplane.Directive {
	return &crossplane.Directive{Directive: name, Args: args, Block: crossplane.Directives(children)}
}

// LocalService is one mesh service co-located on the host being rendered — the callee
// side of the mesh. The proxy terminates peer mTLS for it (selecting its leaf by SNI),
// enforces its allow-list, and forwards to its loopback mesh port over plain HTTP.
type LocalService struct {
	Name           string   // bare service name — deterministic ordering + the allow-map variable
	SNI            string   // the leaf's mesh DNS SAN (meshpaths.DNSName(scope, service)) — server_name / SNI selector
	MeshPort       int      // 127.0.0.1:<MeshPort> the service serves peer traffic on (plain HTTP)
	LeafCertPath   string   // the service's leaf — the mTLS server cert (the mesh holds it)
	LeafKeyPath    string   // the leaf key
	AllowedCallers []string // caller identities (<scope>/<service>, incl. <scope>/gateway) permitted to call it
}

// Config is the full input for a host's mesh nginx. Egress (caller-plane) fields are
// added here so the signature is stable as the renderer grows.
type Config struct {
	// ListenAddr is the address the mTLS ingress binds: the host's private IP on a
	// regional host (peers reach it over the private network), or 0.0.0.0 on the global
	// host (which additionally accepts cross-scope peers publicly — the mesh gateway).
	ListenAddr string
	// TrustBundlePath is the on-host path of the mesh CA bundle used to verify a peer's
	// client cert (ssl_client_certificate) — the credential-free plaintext trust set.
	TrustBundlePath string
	// Local are the mesh services co-located on this host (the callee plane).
	Local []LocalService
}

// Render builds the complete mesh nginx.conf for a host. The output is deterministic
// (services sorted by name, allow-lists sorted) so identical input renders identical
// bytes. It fails loud on an incomplete service rather than emit a config that would
// silently misroute.
func Render(c Config) (string, error) {
	if c.ListenAddr == "" {
		return "", fmt.Errorf("meshnginx: no listen address")
	}
	if len(c.Local) > 0 && c.TrustBundlePath == "" {
		return "", fmt.Errorf("meshnginx: mesh services present but no trust bundle path")
	}
	local := append([]LocalService(nil), c.Local...)
	sort.Slice(local, func(i, j int) bool { return local[i].Name < local[j].Name })
	for _, s := range local {
		if s.SNI == "" || s.MeshPort < 1 || s.LeafCertPath == "" || s.LeafKeyPath == "" {
			return "", fmt.Errorf("meshnginx: mesh service %q is incompletely resolved (sni/port/leaf)", s.Name)
		}
	}

	http := crossplane.Directives{
		// WebSocket upgrade support, applied uniformly to every mesh location (the mesh
		// is a general HTTP/WS proxy; ADR-0032 realization invariant).
		block("map", []string{"$http_upgrade", "$connection_upgrade"},
			dir("default", "upgrade"),
			dir("''", "close"),
		),
		// Extract the peer identity (the cert CN, <scope>/<service>) from the full
		// subject DN nginx exposes ($ssl_client_s_dn is e.g. "CN=us-east-1/ddns").
		block("map", []string{"$ssl_client_s_dn", "$mesh_caller"},
			dir("default", `""`),
			dir(`~^CN=(?<cn>.+)$`, "$cn"),
		),
	}
	// One allow-list map per local service: the peer identity -> permitted (1) or not (0).
	for _, s := range local {
		entries := crossplane.Directives{dir("default", "0")}
		callers := append([]string(nil), s.AllowedCallers...)
		sort.Strings(callers)
		for _, caller := range callers {
			entries = append(entries, dir(caller, "1"))
		}
		http = append(http, block("map", []string{"$mesh_caller", allowVar(s.Name)}, entries...))
	}
	// One mTLS server per local service, demuxed by SNI (server_name = its mesh DNS name).
	for _, s := range local {
		http = append(http, ingressServer(s, c.ListenAddr, c.TrustBundlePath))
	}

	top := crossplane.Directives{
		dir("user", "nginx"),
		dir("worker_processes", "auto"),
		dir("pid", meshpaths.PIDPath),
		block("events", nil, dir("worker_connections", "1024")),
		block("http", nil, http...),
	}
	var sb strings.Builder
	if err := crossplane.Build(&sb, crossplane.Config{Parsed: top}, &crossplane.BuildOptions{Indent: 4}); err != nil {
		return "", fmt.Errorf("meshnginx: build config: %w", err)
	}
	return "# Managed by inforge — do not edit by hand (mesh proxy).\n" + sb.String() + "\n", nil
}

// ingressServer renders the mTLS server for one co-located service: it presents the
// service's leaf (selected by SNI), verifies the peer's client cert against the trust
// bundle, rejects an unverified or disallowed caller, and reverse-proxies to the
// service's loopback mesh port — path untouched (the caller's mesh preserved it),
// injecting the verified identity as X-Service-Identity and carrying the WS upgrade and
// the forwarded client IP.
func ingressServer(s LocalService, listenAddr, bundlePath string) *crossplane.Directive {
	return block("server", nil,
		dir("listen", fmt.Sprintf("%s:%d", listenAddr, meshpaths.MTLSPort), "ssl"),
		dir("server_name", s.SNI),
		dir("ssl_certificate", s.LeafCertPath),
		dir("ssl_certificate_key", s.LeafKeyPath),
		dir("ssl_client_certificate", bundlePath),
		dir("ssl_verify_client", "on"),
		block("location", []string{"/"},
			// Defence in depth: ssl_verify_client already rejects an unverified peer, but
			// a client with a valid-but-unlisted identity must also be refused.
			block("if", []string{allowVar(s.Name), "=", "0"},
				dir("return", "403"),
			),
			dir("proxy_http_version", "1.1"),
			dir("proxy_set_header", "Upgrade", "$http_upgrade"),
			dir("proxy_set_header", "Connection", "$connection_upgrade"),
			dir("proxy_set_header", "X-Service-Identity", "$mesh_caller"),
			dir("proxy_set_header", "X-Forwarded-For", "$proxy_add_x_forwarded_for"),
			// Long-lived streams (e.g. a daemon control WebSocket) must not be dropped.
			dir("proxy_read_timeout", "3600s"),
			dir("proxy_pass", fmt.Sprintf("http://127.0.0.1:%d", s.MeshPort)),
		),
	)
}

// allowVar is the nginx variable (with the leading $) holding a service's allow result.
func allowVar(service string) string { return "$" + allowVarName(service) }

// allowVarName is the bare nginx variable name (no $) for a service's allow map — the
// service name sanitised to the alnum/underscore nginx identifier charset.
func allowVarName(service string) string {
	var b strings.Builder
	b.WriteString("mesh_allow_")
	for _, r := range service {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
