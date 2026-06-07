package caddy

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/wardnet/inforge/internal/types"
)

// L4ConfigPath is the native-JSON Caddy config the layer4 realization runs.
// Path B points the systemd unit at this file (a Caddyfile cannot express the
// layer4 listener wrapper richly enough), distinct from the path-A Caddyfile.
const L4ConfigPath = ConfigDir + "/caddy.json"

// loopbackListen is the address the single edge http server binds. layer4 owns
// the connection-peeking; the same server terminates fall-through TLS — there is
// no separate loopback listener, so :443 is the only bind.
const edgeListen = ":443"

// NeedsL4 reports whether a host's route set requires the layer4-fronted
// realization (path B). A host with only terminate routes keeps the simpler
// Caddyfile/conf.d realization (path A) untouched; any passthrough or catch-all
// route engages layer4.
func NeedsL4(routes []types.TLSRoute) bool {
	for _, r := range routes {
		if r.Catchall || r.Mode == types.IngressTLSPassthrough {
			return true
		}
	}
	return false
}

// Caddy native-JSON config structs. Only the fields inforge sets are modeled;
// everything else relies on Caddy defaults (notably automatic HTTPS, which
// provisions ACME certs for the terminate host routes and manages :80 for the
// HTTP-01 challenge + redirect).

type l4Config struct {
	Apps l4Apps `json:"apps"`
}

type l4Apps struct {
	HTTP l4HTTPApp `json:"http"`
}

type l4HTTPApp struct {
	Servers map[string]l4HTTPServer `json:"servers"`
}

type l4HTTPServer struct {
	Listen           []string      `json:"listen"`
	ListenerWrappers []any         `json:"listener_wrappers,omitempty"`
	Routes           []l4HTTPRoute `json:"routes,omitempty"`
}

// layer4 listener wrapper (caddy.listeners.layer4) and the core tls wrapper.

type l4LayerWrapper struct {
	Wrapper string    `json:"wrapper"` // "layer4"
	Routes  []l4Route `json:"routes"`
}

type tlsWrapper struct {
	Wrapper string `json:"wrapper"` // "tls"
}

type l4Route struct {
	Match  []l4MatcherSet `json:"match,omitempty"`
	Handle []l4Handler    `json:"handle"`
}

type l4MatcherSet struct {
	TLS *l4TLSMatcher  `json:"tls,omitempty"`
	Not []l4MatcherSet `json:"not,omitempty"`
}

type l4TLSMatcher struct {
	SNI []string `json:"sni,omitempty"`
}

type l4Handler struct {
	Handler       string       `json:"handler"`                 // "proxy"
	ProxyProtocol string       `json:"proxy_protocol,omitempty"` // "", "v1", "v2"
	Upstreams     []l4Upstream `json:"upstreams"`
}

type l4Upstream struct {
	Dial []string `json:"dial"`
}

// http terminate routes (Host-matched reverse_proxy after TLS termination).

type l4HTTPRoute struct {
	Match    []l4HTTPMatcherSet `json:"match"`
	Handle   []l4HTTPHandler    `json:"handle"`
	Terminal bool               `json:"terminal,omitempty"`
}

type l4HTTPMatcherSet struct {
	Host []string `json:"host"`
}

type l4HTTPHandler struct {
	Handler   string           `json:"handler"` // "reverse_proxy"
	Upstreams []l4HTTPUpstream `json:"upstreams"`
}

type l4HTTPUpstream struct {
	Dial string `json:"dial"`
}

// RenderL4Config renders the native-JSON Caddy config for a layer4-fronted host.
// Routes are split into terminate (fall through to the tls wrapper + Host-matched
// reverse_proxy), passthrough (matched by SNI, raw-proxied), and the single
// catch-all (matches every SNI not claimed by a terminate route, raw-proxied).
// The output is deterministic: SNIs and routes are sorted, so the same route set
// always renders byte-for-byte the same config.
func RenderL4Config(routes []types.TLSRoute) (string, error) {
	var terminate, passthrough []types.TLSRoute
	var catchall *types.TLSRoute
	for i := range routes {
		r := routes[i]
		switch {
		case r.Catchall:
			catchall = &routes[i]
		case r.Mode == types.IngressTLSPassthrough:
			passthrough = append(passthrough, r)
		default:
			terminate = append(terminate, r)
		}
	}
	sort.Slice(terminate, func(i, j int) bool { return terminate[i].FQDN < terminate[j].FQDN })
	sort.Slice(passthrough, func(i, j int) bool { return passthrough[i].FQDN < passthrough[j].FQDN })

	terminateSNIs := make([]string, 0, len(terminate))
	for _, r := range terminate {
		terminateSNIs = append(terminateSNIs, r.FQDN)
	}

	// layer4 wrapper routes: passthrough first (specific SNIs), catch-all last.
	l4Routes := make([]l4Route, 0, len(passthrough)+1)
	for _, r := range passthrough {
		l4Routes = append(l4Routes, l4Route{
			Match:  []l4MatcherSet{{TLS: &l4TLSMatcher{SNI: []string{r.FQDN}}}},
			Handle: []l4Handler{proxyHandler(r.Port, r.ProxyProtocol)},
		})
	}
	if catchall != nil {
		match := l4MatcherSet{TLS: &l4TLSMatcher{}}
		// Exclude terminate SNIs so they fall through to the tls wrapper instead
		// of being swallowed by the catch-all proxy.
		if len(terminateSNIs) > 0 {
			match.Not = []l4MatcherSet{{TLS: &l4TLSMatcher{SNI: terminateSNIs}}}
		}
		l4Routes = append(l4Routes, l4Route{
			Match:  []l4MatcherSet{match},
			Handle: []l4Handler{proxyHandler(catchall.Port, catchall.ProxyProtocol)},
		})
	}

	// http terminate routes: Host-matched reverse_proxy to the local port.
	httpRoutes := make([]l4HTTPRoute, 0, len(terminate))
	for _, r := range terminate {
		httpRoutes = append(httpRoutes, l4HTTPRoute{
			Match: []l4HTTPMatcherSet{{Host: []string{r.FQDN}}},
			Handle: []l4HTTPHandler{{
				Handler:   "reverse_proxy",
				Upstreams: []l4HTTPUpstream{{Dial: fmt.Sprintf("127.0.0.1:%d", r.Port)}},
			}},
			Terminal: true,
		})
	}

	cfg := l4Config{Apps: l4Apps{HTTP: l4HTTPApp{Servers: map[string]l4HTTPServer{
		"edge": {
			Listen: []string{edgeListen},
			ListenerWrappers: []any{
				l4LayerWrapper{Wrapper: "layer4", Routes: l4Routes},
				tlsWrapper{Wrapper: "tls"},
			},
			Routes: httpRoutes,
		},
	}}}}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render layer4 caddy config: %w", err)
	}
	return string(out) + "\n", nil
}

// proxyHandler builds a layer4 proxy handler dialing a local port, optionally
// prepending the PROXY protocol header so the backend learns the real client.
func proxyHandler(port int, proxyProtocol string) l4Handler {
	return l4Handler{
		Handler:       "proxy",
		ProxyProtocol: proxyProtocol,
		Upstreams:     []l4Upstream{{Dial: []string{fmt.Sprintf("tcp/127.0.0.1:%d", port)}}},
	}
}
