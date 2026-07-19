package program

import (
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/registry"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

// commandType is the Pulumi type token for the pulumi-command remote command
// resource the gate and every per-host SSH step register.
const commandType = "command:remote:Command"

// capturedCommand records the inputs and dependency URNs of one registered
// command.remote, so a test can assert connection identity, the script, and the
// DependsOn wiring without a live host.
type capturedCommand struct {
	create string
	user   string
	deps   []string
	// triggersSecret records whether the command's `triggers` input carried any
	// secret. A secret must never reach Triggers: a secret+unknown trigger marshals
	// to the engine error `malformed RPC secret: missing value for "triggers"` at
	// preview (see safeTrigger).
	triggersSecret bool
	// triggers records the command's resolved string trigger elements, so a test can
	// assert a trigger is the safeTrigger HASH of a script rather than the script
	// itself (the mocked engine resolves a secret value, so secretness alone does not
	// prove the hashing happened).
	triggers []string
	// deleteScript records the command's `delete` input (empty when the command
	// carries none), so a test can assert a command is delete-free (ADR-0042).
	deleteScript string
}

// commandMocks is a Pulumi mock monitor that records every command.remote keyed
// by its logical name. URNs are assigned by the engine; the mock reconstructs a
// matchable URN suffix ("::"+name) so dependency assertions can resolve a name.
type commandMocks struct {
	mu       sync.Mutex
	captured map[string]capturedCommand
}

func newCommandMocks() *commandMocks {
	return &commandMocks{captured: map[string]capturedCommand{}}
}

func (m *commandMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *commandMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == commandType {
		c := capturedCommand{}
		if v, ok := args.Inputs["create"]; ok {
			// A script built inside an apply over a secret (a password, a credential) is
			// itself secret — unwrap before reading it.
			if v.IsSecret() {
				v = v.SecretValue().Element
			}
			if v.IsString() {
				c.create = v.StringValue()
			}
		}
		// The connection carries the private key, so command.remote marks the whole
		// object secret — unwrap it before reading the user.
		if v, ok := args.Inputs["connection"]; ok {
			if v.IsSecret() {
				v = v.SecretValue().Element
			}
			if v.IsObject() {
				if u, ok := v.ObjectValue()["user"]; ok && u.IsString() {
					c.user = u.StringValue()
				}
			}
		}
		if v, ok := args.Inputs["delete"]; ok {
			if v.IsSecret() {
				v = v.SecretValue().Element
			}
			if v.IsString() {
				c.deleteScript = v.StringValue()
			}
		}
		if v, ok := args.Inputs["triggers"]; ok {
			c.triggersSecret = v.ContainsSecrets()
			if v.IsSecret() {
				v = v.SecretValue().Element
			}
			if v.IsArray() {
				for _, e := range v.ArrayValue() {
					if e.IsSecret() {
						e = e.SecretValue().Element
					}
					if e.IsString() {
						c.triggers = append(c.triggers, e.StringValue())
					}
				}
			}
		}
		if args.RegisterRPC != nil {
			c.deps = args.RegisterRPC.GetDependencies()
		}
		m.mu.Lock()
		m.captured[args.Name] = c
		m.mu.Unlock()
	}
	// A host-pubkey read must yield a real SSH key so the downstream secrets-write
	// (age-encrypts the blob to it) resolves instead of erroring on an empty key.
	outputs := resource.PropertyMap{}
	if strings.HasSuffix(args.Name, "-hostkey") {
		outputs["stdout"] = resource.NewStringProperty(testHostPubKey)
	}
	return args.Name + "-id", outputs, nil
}

// testHostPubKey is a valid ssh-ed25519 public key used as a mock host-key read
// output, so the secrets-write path (which age-encrypts to it) resolves in tests.
const testHostPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIImjg7x3zYiZglXWqq+8A9DmqBG+tbjdAzWjT2Pp1X8n"

// dependsOn reports whether the command named cmd lists a dependency whose URN
// resolves to the command named dep (URNs end with "::"+name).
func (m *commandMocks) dependsOn(cmd, dep string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, urn := range m.captured[cmd].deps {
		if strings.HasSuffix(urn, "::"+dep) {
			return true
		}
	}
	return false
}

func gateName(env, slug, hostKey string) string {
	return naming.Resource(env, slug, "vm", hostKey) + "-cloudinit-ready"
}

// TestCloudInitGateShape: the gate connects as root and blocks on
// `cloud-init status --wait`.
func TestCloudInitGateShape(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		host := types.ComputeOutputs{PublicIP: pulumi.String("1.2.3.4").ToStringOutput()}
		_, err := cloudInitGate(ctx, map[string]pulumi.Resource{}, "bridge-01", host, "priv", "prd", "use1")
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	got := mocks.captured[gateName("prd", "use1", "bridge-01")]
	assert.Equal(t, "cloud-init status --wait", got.create)
	assert.Equal(t, "root", got.user, "the gate must connect as root, not the deploy user")
}

// TestCloudInitGateMemoized: a second call for the same host returns the same
// resource and registers no second command.
func TestCloudInitGateMemoized(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		gates := map[string]pulumi.Resource{}
		host := types.ComputeOutputs{PublicIP: pulumi.String("1.2.3.4").ToStringOutput()}
		first, err := cloudInitGate(ctx, gates, "bridge-01", host, "priv", "prd", "use1")
		if err != nil {
			return err
		}
		second, err := cloudInitGate(ctx, gates, "bridge-01", host, "priv", "prd", "use1")
		if err != nil {
			return err
		}
		assert.Same(t, first, second, "same host must reuse the same gate")
		return nil
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	require.Contains(t, mocks.captured, gateName("prd", "use1", "bridge-01"))
	assert.Len(t, mocks.captured, 1, "memoized gate must register exactly one command")
}

// TestProvisioningDependsOnGate: every per-host SSH step a service emits — the
// unit provision and the descriptor/credential write — depends on the host's
// cloud-init gate. (Gate sharing across the TLS and service paths is covered by
// TestTLSAndServiceShareOneGate.)
func TestProvisioningDependsOnGate(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		res := types.Resources{
			Compute: []types.ComputeSpec{
				{Name: "bridge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
			},
			Service: []types.ServiceSpec{
				{Name: "ghost", Container: "ghost", Host: "bridge-01", Type: "raw", User: "ghost"},
			},
		}
		computeOut := map[string]types.ComputeOutputs{
			"bridge-01": {PublicIP: pulumi.String("1.2.3.4").ToStringOutput()},
		}
		gates := map[string]pulumi.Resource{}
		// No bundle for ghost -> secret-less descriptor path.
		return provisionServices(ctx, res, computeOut, map[string]serviceMaterial{}, gates, "priv", "prd", "us-east-1", "use1", "example.com", "1.2.3", nil)
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	gate := gateName("prd", "use1", "bridge-01")
	svc := naming.Resource("prd", "use1", "svc", "ghost")
	require.Contains(t, mocks.captured, gate)
	assert.True(t, mocks.dependsOn(svc+"-provision", gate), "unit provision must wait on the gate")
	// Delivery now waits on the UNIT rather than the gate directly (the unit waits on
	// the gate, so cloud-init ordering is preserved transitively). Its script restarts
	// the service, so it must never run while a provision replace has the unit file
	// removed — see TestDeliveryDependsOnUnitProvision.
	assert.True(t, mocks.dependsOn(svc+"-secrets", svc+"-provision"), "descriptor write must wait on the unit")
	assert.True(t, mocks.dependsOn(svc+"-provision", gate), "…which transitively waits on the gate")
}

// TestTLSAndServiceShareOneGate: TLS realization and service provisioning SSH the
// same host, so they must depend on the *same* gate resource — exactly one
// cloud-init gate is created for the shared host across both paths.
func TestTLSAndServiceShareOneGate(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		reg := registry.BuildRegistry(ctx,
			map[string]map[string]any{"hetzner": {"apiToken": "x"}},
			nil, types.SSHConfig{DeployPrivateKey: "priv"}, nil, "proj", "prd", "us-east-1", tags.Ephemeral{}, nil)
		res := types.Resources{
			Compute: []types.ComputeSpec{
				{Name: "bridge", Provider: "hetzner", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
			},
			// The ingress resource is co-located on bridge, so its nginx realization
			// and the service provisioning SSH the same host and share one gate.
			Ingress: []types.IngressSpec{{Name: "web", Host: "bridge"}},
			Service: []types.ServiceSpec{
				{Name: "ghost", Container: "ghost", Host: "bridge-01", Type: "raw", User: "ghost",
					Ingress: "web",
					Routes:  []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}},
			},
		}
		computeOut := map[string]types.ComputeOutputs{
			"bridge-01": {PublicIP: pulumi.String("1.2.3.4").ToStringOutput()},
		}
		gates := map[string]pulumi.Resource{}
		if err := realizeIngress(ctx, reg, res, computeOut, gates, nil, "priv", "prd", "use1", "example.com", "", types.SecurityConfig{}, types.ProviderDefaults{}); err != nil {
			return err
		}
		return provisionServices(ctx, res, computeOut, map[string]serviceMaterial{}, gates, "priv", "prd", "us-east-1", "use1", "example.com", "1.2.3", nil)
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	mocks.mu.Lock()
	defer mocks.mu.Unlock()
	gateCount := 0
	for name := range mocks.captured {
		if strings.HasSuffix(name, "-cloudinit-ready") {
			gateCount++
		}
	}
	assert.Equal(t, 1, gateCount, "TLS and service provisioning must share one gate per host")
	require.Contains(t, mocks.captured, gateName("prd", "use1", "bridge-01"))
}
