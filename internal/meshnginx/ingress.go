package meshnginx

import (
	"fmt"
	"sort"
	"strings"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/pathglob"
)

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
	Paths          []string // the callee's declared endpoint surface (public ∪ internal path globs, ADR-0034): only matching paths are proxied, anything else is a JSON 404; nil keeps the legacy full-open location (defensive only — validation rejects a path-less callee)
}

// ingressDirectives renders the callee plane: the peer-identity extraction map, one
// allow-list map per co-located service, and one mTLS server per service (demuxed by
// SNI). Returns nil when the host runs no mesh services.
func ingressDirectives(local []LocalService, listenAddr, bundlePath string) ([]*crossplane.Directive, error) {
	if len(local) == 0 {
		return nil, nil
	}
	if bundlePath == "" {
		return nil, fmt.Errorf("meshnginx: mesh services present but no trust bundle path")
	}
	local = append([]LocalService(nil), local...)
	sort.Slice(local, func(i, j int) bool { return local[i].Name < local[j].Name })
	// Compile each callee's declared path globs up front (validation guarantees
	// parseability; a bad glob reaching this far fails the render loud rather than
	// emitting a broken location).
	regexesOf := map[string][]string{}
	for _, s := range local {
		if s.SNI == "" || s.MeshPort < 1 || s.LeafCertPath == "" || s.LeafKeyPath == "" {
			return nil, fmt.Errorf("meshnginx: mesh service %q is incompletely resolved (sni/port/leaf)", s.Name)
		}
		// The callee surface is allowlist-only (ADR-0034). Failing loud here —
		// not falling back to a full-open location — keeps the security property
		// at the enforcement layer: a path-less callee reaching the render (a
		// deploy that skipped validation, a derivation regression) must never
		// silently open every endpoint to allowed peers.
		if len(s.Paths) == 0 {
			return nil, fmt.Errorf("meshnginx: mesh service %q declares no endpoint paths; the callee surface is allowlist-only (declare mesh.public_paths/internal_paths)", s.Name)
		}
		paths := append([]string(nil), s.Paths...)
		sort.Strings(paths)
		for _, raw := range paths {
			p, err := pathglob.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("meshnginx: mesh service %q: invalid path glob %q: %w", s.Name, raw, err)
			}
			regexesOf[s.Name] = append(regexesOf[s.Name], p.Regex())
		}
	}

	// Extract the peer identity (the cert CN, <scope>/<service>) from the full subject
	// DN nginx exposes ($ssl_client_s_dn is e.g. "CN=us-east-1/ddns").
	out := crossplane.Directives{
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
		out = append(out, block("map", []string{"$mesh_caller", allowVar(s.Name)}, entries...))
	}
	// One mTLS server per local service, demuxed by SNI (server_name = its mesh DNS name).
	for _, s := range local {
		out = append(out, ingressServer(s, listenAddr, bundlePath, regexesOf[s.Name]))
	}
	return out, nil
}

// ingressServer renders the mTLS server for one co-located service: it presents the
// service's leaf (selected by SNI), verifies the peer's client cert against the trust
// bundle, rejects an unverified or disallowed caller, and reverse-proxies to the
// service's loopback mesh port — path untouched (the caller's mesh preserved it),
// injecting the verified identity as X-Service-Identity and carrying the WS upgrade and
// the forwarded client IP. Only the callee's declared path globs are proxied
// (ADR-0034): one regex location per compiled glob, and an undeclared path is a
// JSON 404 — even an allowed, authenticated peer cannot reach an endpoint the
// service never declared. ingressDirectives has already rejected an empty
// regexes (allowlist-only, never full-open).
func ingressServer(s LocalService, listenAddr, bundlePath string, regexes []string) *crossplane.Directive {
	proxyLocation := func(matcher []string) *crossplane.Directive {
		return block("location", matcher,
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
		)
	}
	children := crossplane.Directives{
		dir("listen", fmt.Sprintf("%s:%d", listenAddr, meshpaths.MTLSPort), "ssl"),
		dir("server_name", s.SNI),
		dir("ssl_certificate", s.LeafCertPath),
		dir("ssl_certificate_key", s.LeafKeyPath),
		dir("ssl_client_certificate", bundlePath),
		dir("ssl_verify_client", "on"),
	}
	for _, re := range regexes {
		children = append(children, proxyLocation([]string{"~", re}))
	}
	// The mesh fronts REST services, so an undeclared path is a small JSON error
	// (the callee-plane sibling of the gateway's edge 404, ADR-0034). The
	// allow-guard runs here too: a valid-cert peer NOT in allowed_services gets a
	// uniform 403 on every path — without it, 403-on-declared vs 404-on-undeclared
	// would let a disallowed peer enumerate the callee's declared surface.
	children = append(children, block("location", []string{"/"},
		block("if", []string{allowVar(s.Name), "=", "0"},
			dir("return", "403"),
		),
		dir("default_type", "application/json"),
		dir("return", "404", `{"error":"not_found"}`),
	))
	return block("server", nil, children...)
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
