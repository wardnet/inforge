// Package meshnginx renders the per-host east-west mesh proxy config (ADR-0032) —
// a SECOND nginx, separate from the north-south one (internal/nginx), so the mesh
// (which holds the co-located services' leaf keys and makes the east-west authz
// decisions) never shares a process with the internet-facing tier. It is pure
// (Pulumi-free, like internal/nginx): the program derives the inputs and the
// provider writes the output.
//
// It renders both planes:
//
//   - ingress (callee): one mTLS server per co-located mesh service, selected by SNI
//     (the leaf's <service>.<scope>.mesh DNS SAN), verifying the peer's client cert
//     against the mesh trust bundle, enforcing the service's allow-list on the peer
//     identity (the cert CN, <scope>/<service>), and forwarding to the service's
//     loopback port with the identity injected as X-Service-Identity.
//   - egress (caller): one loopback listener per co-located service (its
//     INFORGE_MESH_URL), routing by the X-Mesh-Target header over mTLS to the target —
//     presenting the caller's leaf, verifying the callee's cert by its mesh SNI. Even
//     a co-located target is reached via the host's own private IP so it loops back
//     through the local ingress, keeping the allow-list and identity enforced uniformly.
//
// The global mesh gateway (public SNI passthrough for multi-host global fan-out) is
// rendered separately.
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

// EgressCaller is a co-located service's outbound endpoint: the service dials
// 127.0.0.1:<EgressPort> (its INFORGE_MESH_URL) and names the target in the
// X-Mesh-Target header. The mesh presents this caller's leaf on the onward mTLS hop,
// so the port doubles as the caller's identity (v1 mechanism, ADR-0032).
type EgressCaller struct {
	Name         string // bare service name — deterministic ordering
	EgressPort   int    // 127.0.0.1:<EgressPort> the service dials for all outbound mesh calls
	LeafCertPath string // the caller's leaf — presented as the client cert on the mTLS hop
	LeafKeyPath  string // the caller's leaf key
}

// Target is one mesh service reachable from this host — a routing-table entry keyed by
// the X-Mesh-Target value. It is host-global (independent of the caller): a target is
// reached by mTLS to Addr, whose cert is verified against the trust bundle and matched
// to SNI. Co-located targets use this host's OWN private IP (traffic loops back through
// the local mTLS ingress), so the allow-list and identity-from-cert are enforced
// uniformly — there is no direct-to-service co-located bypass.
type Target struct {
	Name string // the X-Mesh-Target value (bare service name)
	Addr string // "<ip>:<MTLSPort>" — the target host's private IP, this host's own private IP (co-located), or the global mesh gateway's public IP (cross-scope)
	SNI  string // the target leaf's mesh DNS name (meshpaths.DNSName) for proxy_ssl_name verification
}

// Config is the full input for a host's mesh nginx — both planes.
type Config struct {
	// ListenAddr is the address the mTLS ingress binds: the host's private IP on a
	// regional host (peers reach it over the private network), or 0.0.0.0 on the global
	// host (which additionally accepts cross-scope peers publicly — the mesh gateway).
	ListenAddr string
	// TrustBundlePath is the on-host path of the mesh CA bundle, used both to verify a
	// peer's client cert on the ingress (ssl_client_certificate) and to verify a callee's
	// server cert on egress (proxy_ssl_trusted_certificate).
	TrustBundlePath string
	// Local are the mesh services co-located on this host (the callee/ingress plane).
	Local []LocalService
	// Egress are the co-located services' outbound endpoints (the caller/egress plane).
	Egress []EgressCaller
	// Targets is the host-global routing table the egress plane resolves X-Mesh-Target
	// against.
	Targets []Target
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
			dir("default", ""),
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

	// Egress (caller) plane: a host-global routing table (X-Mesh-Target -> mTLS address +
	// SNI) plus one loopback listener per co-located caller, presenting that caller's leaf.
	if len(c.Egress) > 0 {
		if c.TrustBundlePath == "" {
			return "", fmt.Errorf("meshnginx: egress callers present but no trust bundle path")
		}
		targets := append([]Target(nil), c.Targets...)
		sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
		addrEntries := crossplane.Directives{dir("default", "")}
		sniEntries := crossplane.Directives{dir("default", "")}
		for _, t := range targets {
			if t.Addr == "" || t.SNI == "" {
				return "", fmt.Errorf("meshnginx: mesh target %q is incompletely resolved (addr/sni)", t.Name)
			}
			addrEntries = append(addrEntries, dir(t.Name, t.Addr))
			sniEntries = append(sniEntries, dir(t.Name, t.SNI))
		}
		http = append(http,
			block("map", []string{"$http_x_mesh_target", "$mesh_addr"}, addrEntries...),
			block("map", []string{"$http_x_mesh_target", "$mesh_sni"}, sniEntries...),
		)
		egress := append([]EgressCaller(nil), c.Egress...)
		sort.Slice(egress, func(i, j int) bool { return egress[i].Name < egress[j].Name })
		for _, e := range egress {
			if e.EgressPort < 1 || e.LeafCertPath == "" || e.LeafKeyPath == "" {
				return "", fmt.Errorf("meshnginx: egress caller %q is incompletely resolved (port/leaf)", e.Name)
			}
			http = append(http, egressServer(e, c.TrustBundlePath))
		}
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

// egressServer renders the loopback listener a co-located service dials for all
// outbound mesh calls. It routes by X-Mesh-Target (via the host-global $mesh_addr /
// $mesh_sni maps) to the target over mTLS, presenting THIS caller's leaf as the client
// cert and verifying the callee's server cert against the trust bundle by its mesh SNI.
// The path is untouched (variable proxy_pass passes the request URI as-is) so daemon
// PoP survives, and the WS upgrade is carried. Identity is established at the callee's
// ingress from the client cert, so the egress injects no identity header.
func egressServer(e EgressCaller, bundlePath string) *crossplane.Directive {
	return block("server", nil,
		dir("listen", fmt.Sprintf("127.0.0.1:%d", e.EgressPort)),
		block("location", []string{"/"},
			// An unknown/missing X-Mesh-Target maps to the empty address — refuse rather
			// than proxy nowhere.
			block("if", []string{"$mesh_addr", "=", ""},
				dir("return", "502"),
			),
			dir("proxy_http_version", "1.1"),
			dir("proxy_set_header", "Upgrade", "$http_upgrade"),
			dir("proxy_set_header", "Connection", "$connection_upgrade"),
			dir("proxy_ssl_certificate", e.LeafCertPath),
			dir("proxy_ssl_certificate_key", e.LeafKeyPath),
			dir("proxy_ssl_trusted_certificate", bundlePath),
			dir("proxy_ssl_verify", "on"),
			dir("proxy_ssl_name", "$mesh_sni"),
			dir("proxy_ssl_server_name", "on"),
			dir("proxy_read_timeout", "3600s"),
			dir("proxy_pass", "https://$mesh_addr"),
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
