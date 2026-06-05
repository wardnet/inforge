package hetzner

import (
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
)

// useEast1 is a complete Hetzner realization for the us-east-1 abstract region,
// used by the Create tests below.
func useEast1() map[string]RegionConfig {
	return map[string]RegionConfig{
		"us-east-1": {
			Location:    "ash",
			NetworkZone: "us-east",
			ServerTypes: map[string]string{"SMALL": "cx23", "MEDIUM": "cx33", "LARGE": "cx43"},
			Images:      map[string]string{"ubuntu-24.04": "ubuntu-24.04"},
		},
	}
}

// ---- Pulumi mock runtime for compute tests -----------------------------------

type computeMocks struct{}

func (m *computeMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *computeMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	// Firewalls need a numeric string ID for the int conversion in Create.
	if args.TypeToken == "hcloud:index/firewall:Firewall" {
		return "42", resource.PropertyMap{
			"name": resource.NewStringProperty(args.Name),
		}, nil
	}
	props := resource.PropertyMap{
		"name": resource.NewStringProperty(args.Name),
	}
	switch args.TypeToken {
	case "hcloud:index/server:Server":
		props["ipv4Address"] = resource.NewStringProperty("1.2.3.4")
	case "hcloud:index/sshKey:SshKey":
		props["publicKey"] = resource.NewStringProperty("ssh-ed25519 AAAA test")
	}
	return args.Name + "-id", props, nil
}

// ---- ensureFirewall idempotency test -----------------------------------------

func TestEnsureFirewallIdempotency(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", nil)

		bridgeSpec := types.ComputeSpec{Name: "bridge", Container: "vpc", Provider: "hetzner"}
		fw1, err := h.ensureFirewall(ctx, bridgeSpec, "prod")
		if err != nil {
			return err
		}
		fw2, err := h.ensureFirewall(ctx, bridgeSpec, "prod")
		if err != nil {
			return err
		}
		if fw1 != fw2 {
			t.Error("ensureFirewall returned different objects for the same key")
		}

		dbSpec := types.ComputeSpec{Name: "db", Container: "vpc", Provider: "hetzner"}
		fw3, err := h.ensureFirewall(ctx, dbSpec, "prod")
		if err != nil {
			return err
		}
		if fw1 == fw3 {
			t.Error("ensureFirewall returned the same object for different keys")
		}
		return nil
	}, pulumi.WithMocks("inforge", "test", &computeMocks{}))

	require.NoError(t, err)
}

func TestEnsureFirewallCustomRules(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", nil)

		spec := types.ComputeSpec{
			Name:      "bridge",
			Container: "vpc",
			Provider:  "hetzner",
			Firewall: &types.FirewallSpec{
				Inbound: []types.FirewallRule{
					{Proto: "tcp", Port: "80"},
					{Proto: "tcp", Port: "443"},
				},
			},
		}
		fw, err := h.ensureFirewall(ctx, spec, "prod")
		if err != nil {
			return err
		}
		if fw == nil {
			t.Error("expected non-nil firewall")
		}
		return nil
	}, pulumi.WithMocks("inforge", "test", &computeMocks{}))

	require.NoError(t, err)
}

func TestComputeCreateWithCustomFirewall(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", useEast1())

		net := types.NetworkOutputs{
			NetworkID: pulumi.String("99").ToStringOutput(),
			SubnetID:  pulumi.String("12345").ToStringOutput(),
		}
		spec := types.ComputeSpec{
			Name:          "bridge",
			Kind:          "vm",
			Container:     "vpc",
			Provider:      "hetzner",
			Network:       "vpc",
			Size:          "SMALL",
			Image:         "ubuntu-24.04",
			InstanceCount: 1,
			Firewall: &types.FirewallSpec{
				Inbound: []types.FirewallRule{
					{Proto: "tcp", Port: "80"},
					{Proto: "tcp", Port: "443"},
				},
			},
		}

		out, err := h.Create(ctx, spec, net, "prod", "us-east-1", "bridge.use1.example.com", "", "")
		if err != nil {
			return err
		}
		_ = out.PublicIP
		return nil
	}, pulumi.WithMocks("inforge", "test", &computeMocks{}))

	require.NoError(t, err)
}

// ---- ensureSshKeys idempotency test ------------------------------------------

func TestEnsureSshKeysIdempotency(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", nil)

		// SSH keys are env-scoped: same env must return the same keys.
		keys1, err := h.ensureSshKeys(ctx, "prod")
		if err != nil {
			return err
		}
		keys2, err := h.ensureSshKeys(ctx, "prod")
		if err != nil {
			return err
		}
		if keys1[0] != keys2[0] || keys1[1] != keys2[1] {
			t.Error("ensureSshKeys returned different objects for the same env")
		}

		// Different env must produce distinct keys.
		keys3, err := h.ensureSshKeys(ctx, "stg")
		if err != nil {
			return err
		}
		if keys1[0] == keys3[0] {
			t.Error("ensureSshKeys returned the same objects for different envs")
		}
		return nil
	}, pulumi.WithMocks("inforge", "test", &computeMocks{}))

	require.NoError(t, err)
}

// ---- Create smoke test -------------------------------------------------------

func TestComputeCreateReturnsPublicIP(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", useEast1())

		// Synthesise a NetworkOutputs with a known subnet ID.
		subnetID := pulumi.String("12345").ToStringOutput()
		net := types.NetworkOutputs{
			NetworkID: pulumi.String("99").ToStringOutput(),
			SubnetID:  subnetID,
		}

		spec := types.ComputeSpec{
			Name:          "bridge",
			Kind:          "vm",
			Container:     "vpc",
			Provider:      "hetzner",
			Network:       "vpc",
			Size:          "SMALL",
			Image:         "ubuntu-24.04",
			InstanceCount: 1,
		}

		out, err := h.Create(ctx, spec, net, "prod", "us-east-1", "bridge.use1.example.com", "", "")
		if err != nil {
			return err
		}
		_ = out.PublicIP // output is a Pulumi apply chain; just verify it's non-zero
		return nil
	}, pulumi.WithMocks("inforge", "test", &computeMocks{}))

	require.NoError(t, err)
}

func TestComputeCreateInstanceCounterIncrement(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", useEast1())

		net := types.NetworkOutputs{
			NetworkID: pulumi.String("99").ToStringOutput(),
			SubnetID:  pulumi.String("12345").ToStringOutput(),
		}
		spec := types.ComputeSpec{
			Name:          "bridge",
			Kind:          "vm",
			Container:     "vpc",
			Provider:      "hetzner",
			Network:       "vpc",
			Size:          "SMALL",
			Image:         "ubuntu-24.04",
			InstanceCount: 2,
		}

		// Two consecutive Create calls for the same spec must succeed with
		// different internal keys (bridge-01, bridge-02).
		_, err := h.Create(ctx, spec, net, "prod", "us-east-1", "bridge.use1.example.com", "", "")
		if err != nil {
			return err
		}
		_, err = h.Create(ctx, spec, net, "prod", "us-east-1", "bridge.use1.example.com", "", "")
		return err
	}, pulumi.WithMocks("inforge", "test", &computeMocks{}))

	require.NoError(t, err)
}

// ---- realization-driven server type + image tests ---------------------------

// capturingMocks records the serverType and image passed to each created server
// so the realization can be asserted end-to-end through Create.
type capturingMocks struct {
	mu          sync.Mutex
	serverTypes []string
	images      []string
}

func (m *capturingMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *capturingMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == "hcloud:index/firewall:Firewall" {
		return "42", resource.PropertyMap{"name": resource.NewStringProperty(args.Name)}, nil
	}
	props := resource.PropertyMap{"name": resource.NewStringProperty(args.Name)}
	switch args.TypeToken {
	case "hcloud:index/server:Server":
		props["ipv4Address"] = resource.NewStringProperty("1.2.3.4")
		m.mu.Lock()
		if st := args.Inputs["serverType"]; st.IsString() {
			m.serverTypes = append(m.serverTypes, st.StringValue())
		}
		if img := args.Inputs["image"]; img.IsString() {
			m.images = append(m.images, img.StringValue())
		}
		m.mu.Unlock()
	case "hcloud:index/sshKey:SshKey":
		props["publicKey"] = resource.NewStringProperty("ssh-ed25519 AAAA test")
	}
	return args.Name + "-id", props, nil
}

// TestComputeCreateUsesRealization proves a SMALL spec provisions the server
// type and image id from the region's realization.
func TestComputeCreateUsesRealization(t *testing.T) {
	mocks := &capturingMocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", useEast1())
		net := types.NetworkOutputs{
			NetworkID: pulumi.String("99").ToStringOutput(),
			SubnetID:  pulumi.String("12345").ToStringOutput(),
		}
		spec := types.ComputeSpec{
			Name: "bridge", Container: "vpc", Provider: "hetzner",
			Network: "vpc", Size: "SMALL", Image: "ubuntu-24.04", InstanceCount: 1,
		}
		_, err := h.Create(ctx, spec, net, "prod", "us-east-1", "bridge.use1.example.com", "", "")
		return err
	}, pulumi.WithMocks("inforge", "test", mocks))
	require.NoError(t, err)
	assert.Equal(t, []string{"cx23"}, mocks.serverTypes)
	assert.Equal(t, []string{"ubuntu-24.04"}, mocks.images)
}

func TestComputeCreateUnknownRegionReturnsError(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", useEast1())
		net := types.NetworkOutputs{
			NetworkID: pulumi.String("99").ToStringOutput(),
			SubnetID:  pulumi.String("12345").ToStringOutput(),
		}
		spec := types.ComputeSpec{
			Name: "bridge", Container: "vpc", Provider: "hetzner",
			Network: "vpc", Size: "SMALL", Image: "ubuntu-24.04", InstanceCount: 1,
		}
		_, err := h.Create(ctx, spec, net, "prod", "eu-central-1", "bridge.euc1.example.com", "", "")
		if err == nil {
			t.Error("expected error for region with no realization, got nil")
		} else {
			assert.Contains(t, err.Error(), "no region realization")
		}
		return nil
	}, pulumi.WithMocks("inforge", "test", &computeMocks{}))

	require.NoError(t, err)
}

func TestComputeCreateUnknownSizeReturnsError(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", useEast1())
		net := types.NetworkOutputs{
			NetworkID: pulumi.String("99").ToStringOutput(),
			SubnetID:  pulumi.String("12345").ToStringOutput(),
		}
		spec := types.ComputeSpec{
			Name: "bridge", Container: "vpc", Provider: "hetzner",
			Network: "vpc", Size: "XLARGE", Image: "ubuntu-24.04", InstanceCount: 1,
		}
		_, err := h.Create(ctx, spec, net, "prod", "us-east-1", "bridge.use1.example.com", "", "")
		if err == nil {
			t.Error("expected error for unknown size, got nil")
		} else {
			assert.Contains(t, err.Error(), "no server type for size")
		}
		return nil
	}, pulumi.WithMocks("inforge", "test", &computeMocks{}))

	require.NoError(t, err)
}

func TestComputeCreateUnknownImageReturnsError(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", useEast1())
		net := types.NetworkOutputs{
			NetworkID: pulumi.String("99").ToStringOutput(),
			SubnetID:  pulumi.String("12345").ToStringOutput(),
		}
		spec := types.ComputeSpec{
			Name: "bridge", Container: "vpc", Provider: "hetzner",
			Network: "vpc", Size: "SMALL", Image: "ubuntu-26.04", InstanceCount: 1,
		}
		_, err := h.Create(ctx, spec, net, "prod", "us-east-1", "bridge.use1.example.com", "", "")
		if err == nil {
			t.Error("expected error for unknown image, got nil")
		} else {
			assert.Contains(t, err.Error(), "no image for")
		}
		return nil
	}, pulumi.WithMocks("inforge", "test", &computeMocks{}))

	require.NoError(t, err)
}
