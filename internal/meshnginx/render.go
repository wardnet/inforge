// Package meshnginx renders the per-host east-west mesh proxy config (ADR-0032) —
// a SECOND nginx, separate from the north-south one (internal/nginx), so the mesh
// (which holds the co-located services' leaf keys and makes the east-west authz
// decisions) never shares a process with the internet-facing tier. It is pure
// (Pulumi-free, like internal/nginx): the program derives the inputs and the
// provider writes the output.
//
// The mesh nginx is ONE instance with two planes — rendered from the two files
// alongside this one:
//
//   - ingress (callee, ingress.go): one mTLS server per co-located mesh service,
//     selected by SNI, verifying the peer client cert, enforcing the allow-list, and
//     forwarding to the service's loopback port with the identity injected.
//   - egress (caller, egress.go): one loopback listener per co-located service (its
//     INFORGE_MESH_URL), routing by the X-Mesh-Target header over mTLS to the target.
//
// The two planes share the same leaf keys (client cert on egress, server cert on
// ingress) and trust domain, so they are one nginx, not two. The global mesh gateway
// (public SNI passthrough for multi-host global fan-out) is rendered separately.
package meshnginx

import (
	"fmt"
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

// Render builds the complete mesh nginx.conf for a host from both planes. The output
// is deterministic (services/targets sorted by name, allow-lists sorted) so identical
// input renders identical bytes. It fails loud on an incompletely resolved input rather
// than emit a config that would silently misroute.
func Render(c Config) (string, error) {
	if c.ListenAddr == "" {
		return "", fmt.Errorf("meshnginx: no listen address")
	}
	http := crossplane.Directives{
		// WebSocket upgrade support, applied uniformly to every mesh location (the mesh
		// is a general HTTP/WS proxy; ADR-0032 realization invariant).
		block("map", []string{"$http_upgrade", "$connection_upgrade"},
			dir("default", "upgrade"),
			dir("''", "close"),
		),
	}
	ing, err := ingressDirectives(c.Local, c.ListenAddr, c.TrustBundlePath)
	if err != nil {
		return "", err
	}
	http = append(http, ing...)
	egr, err := egressDirectives(c.Egress, c.Targets, c.TrustBundlePath)
	if err != nil {
		return "", err
	}
	http = append(http, egr...)

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
