package hetzner

import (
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/nginx"
	"github.com/wardnet/inforge/internal/types"
)

// awaitScript resolves a rendered write-script (a plain string on the co-located
// fast path, a StringOutput on the cross-host apply path) and returns its content.
// ApplyT is asynchronous, so the test must WAIT for it or race the resolution.
func awaitScript(t *testing.T, in pulumi.StringInput) string {
	t.Helper()
	var got string
	var wg sync.WaitGroup
	wg.Add(1)
	in.ToStringOutput().ApplyT(func(s string) string { got = s; wg.Done(); return s })
	wg.Wait()
	return got
}

// TestRenderWriteScriptGateway exercises both halves of a private gateway (ADR-0045)
// through renderWriteScript: the co-located FAST path (loopback addresses, synchronous)
// and the SPLIT apply path (the termination's Backend, the routing server's own-host
// ListenAddr, and its ingress-trusting RealIPFrom all resolved from private IPs). A
// non-empty result proves the fill logic resolved every address and nginx.Render
// succeeded — Render/renderWriteScript return an error if any address is unresolved.
func TestRenderWriteScriptGateway(t *testing.T) {
	fqdn := "api.use1.wardnet.network"
	routes := []types.IngressGatewayRoute{{Pattern: "/x/**", Service: "svc"}}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewTLS("deploy-key", "use1")

		// Fast path: co-located gateway — every address is loopback, so the config
		// renders synchronously with no apply over unknown private IPs.
		coTerm := types.IngressGateway{Name: "api", FQDN: fqdn, Backend: "127.0.0.1", HTTPPort: nginx.GatewayHTTPPort}
		coRoute := types.IngressGateway{Name: "api", FQDN: fqdn, HTTPPort: nginx.GatewayHTTPPort,
			ListenAddr: "127.0.0.1", RealIPFrom: "127.0.0.1", Routes: routes}
		fast, err := h.renderWriteScript("edge-01", nil, nil, nil, 0,
			[]types.IngressGateway{coTerm}, []types.IngressGateway{coRoute}, nil, nil,
			pulumi.String("10.0.0.5").ToStringOutput())
		require.NoError(t, err)
		assert.NotEmpty(t, awaitScript(t, fast), "co-located gateway renders synchronously")

		// Split path: the termination's Backend and the routing's RealIPFrom resolve from
		// the gateway's peer IP; the routing's ListenAddr resolves from the host's own
		// private IP. Empty fields force the apply branch that fills them.
		splitTerm := types.IngressGateway{Name: "api", FQDN: fqdn, HTTPPort: nginx.GatewayHTTPPort}
		splitRoute := types.IngressGateway{Name: "api", FQDN: fqdn, HTTPPort: nginx.GatewayHTTPPort, Routes: routes}
		gwIPs := map[string]pulumi.StringOutput{"api": pulumi.String("10.0.0.9").ToStringOutput()}
		split, err := h.renderWriteScript("edge-01", nil, nil, nil, 0,
			[]types.IngressGateway{splitTerm}, []types.IngressGateway{splitRoute}, nil, gwIPs,
			pulumi.String("10.0.0.5").ToStringOutput())
		require.NoError(t, err)
		assert.NotEmpty(t, awaitScript(t, split), "split gateway resolves every private IP and renders")
		return nil
	}, pulumi.WithMocks("project", "stack", &testMocks{}))
	require.NoError(t, err)
}
