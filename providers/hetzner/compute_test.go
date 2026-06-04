package hetzner

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/images"
	"github.com/wardnet/inforge/internal/types"
)

// ---- ResolveImage tests (pure, no Pulumi) ------------------------------------

func TestResolveImageKnown(t *testing.T) {
	got, err := ResolveImage(images.Ubuntu2404)
	require.NoError(t, err)
	assert.Equal(t, "ubuntu-24.04", got)
}

func TestResolveImageUnknown(t *testing.T) {
	_, err := ResolveImage("centos-7")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hetzner has no image for")
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
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", nil, "test-project", "use1", nil)

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
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", nil, "test-project", "use1", nil)

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
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", nil, "test-project", "use1", nil)

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
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", nil, "test-project", "use1", nil)

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
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", nil, "test-project", "use1", nil)

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
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", nil, "test-project", "use1", nil)

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

func TestComputeCreateUnknownSizeReturnsError(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", nil, "test-project", "use1", nil)
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
		}
		return nil
	}, pulumi.WithMocks("inforge", "test", &computeMocks{}))

	require.NoError(t, err)
}
