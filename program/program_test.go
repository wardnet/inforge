package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/bootstrapper"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/service"
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
	script := serviceProvisionScript(types.ServiceSpec{Name: "api", User: "svc"}, "1.2.3")

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
	script := serviceProvisionScript(types.ServiceSpec{Name: "api"}, "1.2.3")
	assert.NotContains(t, script, "useradd", "no user declared -> no useradd")
}

// TestServiceProvisionScriptDownloadsBootstrapper guards that provisioning
// downloads the inforge-bootstrap binary, pinned to the inforge version, with
// host-side arch detection and the version quoted (injection-safe).
func TestServiceProvisionScriptDownloadsBootstrapper(t *testing.T) {
	script := serviceProvisionScript(types.ServiceSpec{Name: "api", User: "svc"}, "1.2.3")

	// Version is single-quoted into a shell var, then composed with ${arch}.
	assert.Contains(t, script, "ver='1.2.3'")
	assert.Contains(t, script, "arch=$(uname -m)")
	assert.Contains(t, script, "x86_64) arch=amd64")
	assert.Contains(t, script, "aarch64) arch=arm64")
	assert.Contains(t, script, "asset=\"inforge-bootstrap_${ver}_linux_${arch}\"")
	assert.Contains(t, script, "releases/download/v${ver}")
	assert.Contains(t, script, "curl -fsSL")
	// The download must be checksum-verified before it is installed as root.
	assert.Contains(t, script, "checksums.txt")
	assert.Contains(t, script, "sha256sum")
	assert.Contains(t, script, "checksum mismatch")
	assert.Contains(t, script, "trap 'rm -f \"$tmp\" \"$sums\"' EXIT", "temp files cleaned on any exit")
	assert.Contains(t, script, "install -m 0755 \"$tmp\" '/usr/local/bin/inforge-bootstrap'")
	// The raw inforge version must never be interpolated unquoted into the shell.
	assert.NotContains(t, script, "inforge-bootstrap_1.2.3_linux")
}

// TestRenderDescriptorRoundTrips proves the descriptor inforge writes parses
// back through the bootstrapper's own validator — the producer (this program)
// and consumer (inforge-bootstrap) cannot drift because both use the same
// Descriptor struct and SupportedVersion constant.
func TestRenderDescriptorRoundTrips(t *testing.T) {
	svc := types.ServiceSpec{Name: "ghost", Container: "ghost", User: "ghost"}
	bundle := &types.ServiceSecretsBundle{
		ProviderKind: "infisical",
		URL:          "https://app.infisical.com",
		Environment:  "prod",
		SecretPath:   "/ghost",
		Env:          map[string]string{"DATABASE_URL": "infra/DATABASE_URL"},
	}

	out, err := renderDescriptor(svc, bundle, "ws-123")
	require.NoError(t, err)

	d, err := bootstrapper.ParseDescriptor([]byte(out))
	require.NoError(t, err)
	assert.Equal(t, bootstrapper.SupportedVersion, d.Version)
	assert.Equal(t, "ghost", d.Service)
	assert.Equal(t, service.ExecPath("ghost"), d.Exec)
	assert.Equal(t, "ghost", d.User)
	assert.Equal(t, "infisical", d.Provider.Kind)
	assert.Equal(t, "ws-123", d.Provider.Project) // project is the workspace id
	assert.Equal(t, "prod", d.Provider.Environment)
	assert.Equal(t, "/ghost", d.Provider.SecretPath)
	assert.Equal(t, "infra/DATABASE_URL", d.Env["DATABASE_URL"])
}

func TestServiceSecretsProviderName(t *testing.T) {
	res := types.Resources{
		Secrets: []types.SecretsSpec{{Container: "ghost", Provider: "infisical"}},
	}
	assert.Equal(t, "infisical", serviceSecretsProviderName(types.ServiceSpec{Container: "ghost"}, res))
	assert.Equal(t, "", serviceSecretsProviderName(types.ServiceSpec{Container: "other"}, res))
}
