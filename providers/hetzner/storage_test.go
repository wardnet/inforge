package hetzner

import (
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

// storageMocks captures the volume + attachment inputs so the tests can assert the
// size floor, resource name, and the server/volume wiring. Server/volume IDs are
// numeric so the idToInt conversions in CreateVolume succeed.
type storageMocks struct {
	mu          sync.Mutex
	volumes     []resource.PropertyMap
	attachments []resource.PropertyMap
}

func (m *storageMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *storageMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	id := args.Name + "-id"
	props := resource.PropertyMap{"name": resource.NewStringProperty(args.Name)}
	switch args.TypeToken {
	case "hcloud:index/firewall:Firewall":
		return "42", props, nil
	case "hcloud:index/placementGroup:PlacementGroup":
		return "88", props, nil
	case "hcloud:index/server:Server":
		id = "77"
		props["ipv4Address"] = resource.NewStringProperty("1.2.3.4")
	case "hcloud:index/sshKey:SshKey":
		props["publicKey"] = resource.NewStringProperty("ssh-ed25519 AAAA test")
	case "hcloud:index/volume:Volume":
		id = "55"
		props["linuxDevice"] = resource.NewStringProperty("/dev/disk/by-id/scsi-0HC_Volume_55")
		m.mu.Lock()
		m.volumes = append(m.volumes, args.Inputs)
		m.mu.Unlock()
	case "hcloud:index/volumeAttachment:VolumeAttachment":
		m.mu.Lock()
		m.attachments = append(m.attachments, args.Inputs)
		m.mu.Unlock()
	}
	return id, props, nil
}

func TestCreateVolumeFloorsAndAttaches(t *testing.T) {
	mocks := &storageMocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("ssh-ed25519 user", "ssh-ed25519 deploy", "", nil, "test-project", "use1", tags.Ephemeral{}, useEast1(), nil)
		spec := types.ComputeSpec{
			Name: "db", Container: "edge", Provider: "hetzner",
			Network: "vpc", Size: "SMALL", Image: "ubuntu-24.04", InstanceCount: 1,
		}
		if _, err := h.Create(ctx, spec, computeNet(), "prod", "us-east-1", "db.use1.example.com", "", types.FirewallPorts{}); err != nil {
			return err
		}
		out, err := h.CreateVolume(ctx, types.StorageRequest{
			Name: "edge", Env: "prod", Region: "us-east-1", Container: "edge",
			HostSpecKey: naming.SpecKey("db", 1), SizeGB: 3, // below the Hetzner minimum → floored
		}, nil)
		require.NoError(t, err)
		if out.DevicePath == (pulumi.StringOutput{}) {
			t.Error("DevicePath output must be wired from the volume")
		}
		return nil
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	require.Len(t, mocks.volumes, 1)
	assert.EqualValues(t, hetznerMinVolumeGB, mocks.volumes[0]["size"].NumberValue(), "sub-minimum size must be floored")
	assert.Equal(t, "wardnet-prod-use1-vol-edge", mocks.volumes[0]["name"].StringValue())
	assert.Equal(t, "ash", mocks.volumes[0]["location"].StringValue(), "volume co-located with the server")

	require.Len(t, mocks.attachments, 1)
	assert.EqualValues(t, 55, mocks.attachments[0]["volumeId"].NumberValue())
	assert.EqualValues(t, 77, mocks.attachments[0]["serverId"].NumberValue())
	assert.False(t, mocks.attachments[0]["automount"].BoolValue(), "inforge owns mkfs/mount, not Hetzner automount")
}

func TestCreateVolumeHonoursLargerSize(t *testing.T) {
	mocks := &storageMocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("u", "d", "", nil, "p", "use1", tags.Ephemeral{}, useEast1(), nil)
		spec := types.ComputeSpec{Name: "db", Container: "edge", Network: "vpc", Size: "SMALL", Image: "ubuntu-24.04", InstanceCount: 1}
		if _, err := h.Create(ctx, spec, computeNet(), "prod", "us-east-1", "db.use1.example.com", "", types.FirewallPorts{}); err != nil {
			return err
		}
		_, err := h.CreateVolume(ctx, types.StorageRequest{Name: "edge", Env: "prod", Region: "us-east-1", HostSpecKey: naming.SpecKey("db", 1), SizeGB: 50}, nil)
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)
	require.Len(t, mocks.volumes, 1)
	assert.EqualValues(t, 50, mocks.volumes[0]["size"].NumberValue(), "a size above the minimum is honoured")
}

func TestCreateVolumeUnknownHost(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		h := NewCompute("u", "d", "", nil, "p", "use1", tags.Ephemeral{}, useEast1(), nil)
		_, err := h.CreateVolume(ctx, types.StorageRequest{
			Name: "edge", Env: "prod", Region: "us-east-1", HostSpecKey: "missing-01", SizeGB: 10,
		}, nil)
		require.Error(t, err, "a volume for a host Create never built must error")
		return nil
	}, pulumi.WithMocks("project", "stack", &storageMocks{}))
	require.NoError(t, err)
}
