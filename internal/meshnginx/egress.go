package meshnginx

import (
	"fmt"
	"sort"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
)

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

// egressDirectives renders the caller plane: the host-global routing table (two maps
// keyed on X-Mesh-Target — address and SNI) and one loopback listener per co-located
// caller. Returns nil when the host runs no mesh callers.
func egressDirectives(egress []EgressCaller, targets []Target, bundlePath string) ([]*crossplane.Directive, error) {
	if len(egress) == 0 {
		return nil, nil
	}
	if bundlePath == "" {
		return nil, fmt.Errorf("meshnginx: egress callers present but no trust bundle path")
	}
	targets = append([]Target(nil), targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	addrEntries := crossplane.Directives{dir("default", "")}
	sniEntries := crossplane.Directives{dir("default", "")}
	for _, t := range targets {
		if t.Addr == "" || t.SNI == "" {
			return nil, fmt.Errorf("meshnginx: mesh target %q is incompletely resolved (addr/sni)", t.Name)
		}
		addrEntries = append(addrEntries, dir(t.Name, t.Addr))
		sniEntries = append(sniEntries, dir(t.Name, t.SNI))
	}
	out := crossplane.Directives{
		block("map", []string{"$http_x_mesh_target", "$mesh_addr"}, addrEntries...),
		block("map", []string{"$http_x_mesh_target", "$mesh_sni"}, sniEntries...),
	}
	egress = append([]EgressCaller(nil), egress...)
	sort.Slice(egress, func(i, j int) bool { return egress[i].Name < egress[j].Name })
	for _, e := range egress {
		if e.EgressPort < 1 || e.LeafCertPath == "" || e.LeafKeyPath == "" {
			return nil, fmt.Errorf("meshnginx: egress caller %q is incompletely resolved (port/leaf)", e.Name)
		}
		out = append(out, egressServer(e, bundlePath))
	}
	return out, nil
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
