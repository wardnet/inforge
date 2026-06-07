package caddy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
)

func TestNeedsL4(t *testing.T) {
	terminateOnly := []types.TLSRoute{
		{Service: "a", FQDN: "a.example", Port: 80, Mode: types.IngressTLSTerminate},
		{Service: "b", FQDN: "b.example", Port: 81, Mode: types.IngressTLSTerminate},
	}
	assert.False(t, NeedsL4(terminateOnly), "terminate-only host stays on the Caddyfile path")

	assert.True(t, NeedsL4(append(terminateOnly,
		types.TLSRoute{Service: "c", FQDN: "c.example", Port: 82, Mode: types.IngressTLSPassthrough})))
	assert.True(t, NeedsL4([]types.TLSRoute{
		{Service: "d", Port: 83, Mode: types.IngressTLSPassthrough, Catchall: true, ProxyProtocol: "v2"}}))
}

// renders the full route mix and asserts the structural contract the Caddy l4
// build relies on, by unmarshaling back into the config model.
func TestRenderL4Config(t *testing.T) {
	routes := []types.TLSRoute{
		{Service: "web", FQDN: "web.prd.use1.wardnet.network", Port: 8080, Mode: types.IngressTLSTerminate},
		{Service: "db", FQDN: "db.prd.use1.wardnet.network", Port: 5432, Mode: types.IngressTLSPassthrough, ProxyProtocol: "v2"},
		{Service: "dispatch", Port: 9000, Mode: types.IngressTLSPassthrough, Catchall: true, ProxyProtocol: "v2"},
	}
	out, err := RenderL4Config(routes)
	require.NoError(t, err)

	var cfg l4Config
	require.NoError(t, json.Unmarshal([]byte(out), &cfg))

	edge, ok := cfg.Apps.HTTP.Servers["edge"]
	require.True(t, ok, "edge server must exist")
	assert.Equal(t, []string{":443"}, edge.Listen)

	// Two listener wrappers in order: layer4 then the core tls wrapper.
	require.Len(t, edge.ListenerWrappers, 2)
	layer4, tls := decodeWrappers(t, edge.ListenerWrappers)
	assert.Equal(t, "layer4", layer4.Wrapper)
	assert.Equal(t, "tls", tls.Wrapper)

	// layer4 routes: passthrough (by SNI) first, catch-all last.
	require.Len(t, layer4.Routes, 2)
	pt := layer4.Routes[0]
	require.NotNil(t, pt.Match[0].TLS)
	assert.Equal(t, []string{"db.prd.use1.wardnet.network"}, pt.Match[0].TLS.SNI)
	assert.Equal(t, "proxy", pt.Handle[0].Handler)
	assert.Equal(t, "v2", pt.Handle[0].ProxyProtocol)
	assert.Equal(t, []string{"tcp/127.0.0.1:5432"}, pt.Handle[0].Upstreams[0].Dial)

	ca := layer4.Routes[1]
	require.NotNil(t, ca.Match[0].TLS, "catch-all matches any TLS")
	assert.Empty(t, ca.Match[0].TLS.SNI)
	// ...but excludes terminate SNIs so they fall through to the tls wrapper.
	require.Len(t, ca.Match[0].Not, 1)
	assert.Equal(t, []string{"web.prd.use1.wardnet.network"}, ca.Match[0].Not[0].TLS.SNI)
	assert.Equal(t, []string{"tcp/127.0.0.1:9000"}, ca.Handle[0].Upstreams[0].Dial)
	assert.Equal(t, "v2", ca.Handle[0].ProxyProtocol)

	// terminate routes live on the http server (reverse_proxy after termination).
	require.Len(t, edge.Routes, 1)
	assert.Equal(t, []string{"web.prd.use1.wardnet.network"}, edge.Routes[0].Match[0].Host)
	assert.Equal(t, "reverse_proxy", edge.Routes[0].Handle[0].Handler)
	assert.Equal(t, "127.0.0.1:8080", edge.Routes[0].Handle[0].Upstreams[0].Dial)
	assert.True(t, edge.Routes[0].Terminal)
}

// A passthrough host with no terminate routes: catch-all has no `not` exclusion.
func TestRenderL4ConfigNoTerminate(t *testing.T) {
	out, err := RenderL4Config([]types.TLSRoute{
		{Service: "dispatch", Port: 9000, Mode: types.IngressTLSPassthrough, Catchall: true, ProxyProtocol: "v2"},
	})
	require.NoError(t, err)

	var cfg l4Config
	require.NoError(t, json.Unmarshal([]byte(out), &cfg))
	edge := cfg.Apps.HTTP.Servers["edge"]
	assert.Empty(t, edge.Routes, "no terminate routes")
	layer4, _ := decodeWrappers(t, edge.ListenerWrappers)
	require.Len(t, layer4.Routes, 1)
	assert.Empty(t, layer4.Routes[0].Match[0].Not, "no terminate SNIs to exclude")
}

func TestRenderL4ConfigDeterministic(t *testing.T) {
	routes := []types.TLSRoute{
		{Service: "z", FQDN: "z.example", Port: 1, Mode: types.IngressTLSPassthrough},
		{Service: "a", FQDN: "a.example", Port: 2, Mode: types.IngressTLSPassthrough},
		{Service: "m", FQDN: "m.example", Port: 3, Mode: types.IngressTLSTerminate},
	}
	first, err := RenderL4Config(routes)
	require.NoError(t, err)
	// Reversed input must render identically (sorted internally).
	reversed := []types.TLSRoute{routes[2], routes[1], routes[0]}
	second, err := RenderL4Config(reversed)
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

// InstallScriptL4 downloads a layer4 build and points the unit at the JSON config.
func TestInstallScriptL4(t *testing.T) {
	s := InstallScriptL4()
	assert.True(t, strings.HasPrefix(s, baseInstallScript), "extends the base install")
	assert.Contains(t, s, "p=github.com/mholt/caddy-l4", "fetches the layer4 module build")
	assert.Contains(t, s, "list-modules", "idempotent: skips download when module present")
	assert.Contains(t, s, L4ConfigPath, "unit points at the native-JSON config")
	assert.Contains(t, s, "caddy run --environ --config "+L4ConfigPath)
}

func decodeWrappers(t *testing.T, raw []any) (l4LayerWrapper, tlsWrapper) {
	t.Helper()
	// Round-trip the generic decode back into the typed wrappers.
	b, err := json.Marshal(raw)
	require.NoError(t, err)
	var generic []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &generic))
	require.Len(t, generic, 2)

	var layer4 l4LayerWrapper
	require.NoError(t, json.Unmarshal(mustWrapper(t, generic[0]), &layer4))
	var tls tlsWrapper
	require.NoError(t, json.Unmarshal(mustWrapper(t, generic[1]), &tls))
	return layer4, tls
}

func mustWrapper(t *testing.T, m map[string]json.RawMessage) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}
