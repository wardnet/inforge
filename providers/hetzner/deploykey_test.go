package hetzner

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/meshnginx"
	"github.com/wardnet/inforge/internal/types"
)

// The ingress and mesh proxies both realize themselves over SSH, so both refuse
// to render without the deploy key — on a PREVIEW as much as an up. The key is
// an input to every command resource they register, so realizing with an empty
// one previews a spurious update against the key already in state (see
// program.resolveDeployKey). Both guards therefore sit outside the !DryRun
// block; these tests pin that, since a plain `ctx.DryRun()` reflex would put
// them back inside it.
func TestRealizeRequiresDeployKey(t *testing.T) {
	host := types.ComputeOutputs{PublicIP: pulumi.String("203.0.113.10").ToStringOutput()}

	t.Run("ingress", func(t *testing.T) {
		err := pulumi.RunErr(func(ctx *pulumi.Context) error {
			h := NewTLS("", "use1")
			err := h.Realize(ctx, "edge-01", host, "deploy",
				[]types.IngressRoute{{Service: "api", Type: types.IngressTypeTLSTermination,
					Listen: 443, Target: 8080, Backend: "127.0.0.1", FQDNs: []string{"api.wardnet.network"}}},
				nil, nil, 0, nil, nil, "prd", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no deploy private key configured")
			return nil
		}, pulumi.WithMocks("project", "stack", &testMocks{}))
		require.NoError(t, err)
	})

	t.Run("mesh", func(t *testing.T) {
		err := pulumi.RunErr(func(ctx *pulumi.Context) error {
			h := NewMesh("", "use1")
			err := h.Realize(ctx, "edge-01", host, "deploy", meshnginx.Config{},
				pulumi.String("10.0.0.2"), nil, "prd", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no deploy private key configured")
			return nil
		}, pulumi.WithMocks("project", "stack", &testMocks{}))
		require.NoError(t, err)
	})
}
