package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/types"
)

func TestDeployUsersByHost(t *testing.T) {
	computes := []types.ComputeSpec{
		{Name: "bridge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
		{Name: "worker", InstanceCount: 2}, // no deploy user
	}
	got := deployUsersByHost(computes)

	assert.Equal(t, "deploy", got["bridge-01"])
	assert.Equal(t, "", got["worker-01"])
	assert.Equal(t, "", got["worker-02"])
	// The bare name is not a host key — only expanded specKeys are.
	_, ok := got["bridge"]
	assert.False(t, ok)
}

func TestVhostsByHostDerivesEnvScopedFQDN(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			// Two ingress services on the same host, declared out of order, to
			// exercise grouping + stable sorting. One service has no ingress.
			{Name: "web", Host: "bridge-01", Ingress: &types.IngressSpec{Hostname: "web", Port: 3000}},
			{Name: "api", Host: "bridge", Ingress: &types.IngressSpec{Hostname: "api", Port: 8080}},
			{Name: "worker", Host: "bridge-01"},
		},
	}
	canonical := naming.CanonicalComputeKeys(res.Compute)

	got := vhostsByHost(res, canonical, "prd", "use1", "wardnet.network")

	// `host: bridge` and `host: bridge-01` both land on the same canonical host.
	require.Len(t, got, 1)
	vhosts := got["bridge-01"]
	require.Len(t, vhosts, 2)

	// Sorted by service name: api before web.
	assert.Equal(t, "api", vhosts[0].Service)
	assert.Equal(t, "api.prd.use1.wardnet.network", vhosts[0].FQDN)
	assert.Equal(t, 8080, vhosts[0].Port)

	assert.Equal(t, "web", vhosts[1].Service)
	assert.Equal(t, "web.prd.use1.wardnet.network", vhosts[1].FQDN)
	assert.Equal(t, 3000, vhosts[1].Port)

	// The FQDN matches RecordFQDN exactly — derivation lives in one place.
	assert.Equal(t, naming.RecordFQDN("prd", "use1", "api", "wardnet.network"), vhosts[0].FQDN)
}

func TestVhostsByHostNoIngressNoVhosts(t *testing.T) {
	res := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{{Name: "worker", Host: "bridge-01"}},
	}
	got := vhostsByHost(res, naming.CanonicalComputeKeys(res.Compute), "prd", "use1", "wardnet.network")
	assert.Empty(t, got)
}

// TestServiceProvisionScriptEnablesNeverStarts guards the headline constraint:
// provisioning writes + enables the unit but must NEVER start/restart it —
// ExecStart=<folder>/run doesn't exist until release delivers code, so a start
// here would fail the whole deploy.
func TestServiceProvisionScriptEnablesNeverStarts(t *testing.T) {
	script := serviceProvisionScript(types.ServiceSpec{Name: "api", User: "svc"})

	assert.Contains(t, script, "systemctl daemon-reload")
	assert.Contains(t, script, "systemctl enable 'wardnet-api.service'")
	assert.NotContains(t, script, "systemctl start", "provisioning must not start the unit")
	assert.NotContains(t, script, "systemctl restart", "provisioning must not restart the unit")
	assert.NotContains(t, script, "enable --now", "enable must not start the unit")

	// Writes the unit file and creates the service folder + user.
	assert.Contains(t, script, "/etc/systemd/system/wardnet-api.service")
	assert.Contains(t, script, "/srv/wardnet/api")
	assert.Contains(t, script, "useradd --system --shell /usr/sbin/nologin 'svc'")
}

func TestServiceProvisionScriptNoUser(t *testing.T) {
	script := serviceProvisionScript(types.ServiceSpec{Name: "api"})
	assert.NotContains(t, script, "useradd", "no user declared -> no useradd")
}
