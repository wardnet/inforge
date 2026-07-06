package hetzner

import (
	"fmt"

	hcloud "github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

// hetznerMinVolumeGB is Hetzner Cloud's minimum block-volume size. A smaller request
// is floored to it: the provider owns its own minimum, so the caller passes the raw
// sum of a cluster's database sizes and CreateVolume applies the floor (ADR-0036:
// cluster volume = max(sum of database sizes, providerMin)).
const hetznerMinVolumeGB = 10

// CreateVolume provisions a persistent Hetzner block volume for a compute host and
// attaches it AFTER the host's cloud-init gate (mirroring AttachNetwork). The volume is
// created unformatted (Format unset) — the caller mkfs/mounts it on-host, so a
// reattached, already-formatted volume is never reformatted (the durability guarantee).
// It returns the stable on-host device path (/dev/disk/by-id/scsi-0HC_Volume_<id>).
func (h *HetznerCompute) CreateVolume(ctx *pulumi.Context, req types.StorageRequest, dependsOn []pulumi.Resource) (types.StorageOutputs, error) {
	// No provider guard — the registry dispatched by resolved provider (see Create).
	regionCfg, err := ResolveRegion(req.Region, h.regions)
	if err != nil {
		return types.StorageOutputs{}, err
	}

	h.mu.Lock()
	sh, ok := h.servers[req.HostSpecKey]
	h.mu.Unlock()
	if !ok {
		return types.StorageOutputs{}, fmt.Errorf("create volume: host %q was not created by Create", req.HostSpecKey)
	}

	size := req.SizeGB
	if size < hetznerMinVolumeGB {
		size = hetznerMinVolumeGB
	}

	volName := naming.Resource(req.Env, h.slug, "vol", req.Name)
	vol, err := hcloud.NewVolume(ctx, volName, &hcloud.VolumeArgs{
		Name:     pulumi.String(volName),
		Size:     pulumi.Int(size),
		Location: pulumi.String(regionCfg.Location),
		Labels:   toStringMap(tags.HetznerLabels(h.project, req.Env, h.slug, req.Container, h.eph)),
	}, h.providerOpts()...)
	if err != nil {
		return types.StorageOutputs{}, fmt.Errorf("create volume %s: %w", volName, err)
	}

	// Attach as a separate resource gated on the cloud-init readiness gate; Automount
	// off because inforge owns the mkfs/mount lifecycle on-host.
	if _, err := hcloud.NewVolumeAttachment(ctx, volName+"-attach", &hcloud.VolumeAttachmentArgs{
		VolumeId:  idToInt(vol.ID()),
		ServerId:  idToInt(sh.server.ID()),
		Automount: pulumi.Bool(false),
	}, append(h.providerOpts(), pulumi.DependsOn(dependsOn))...); err != nil {
		return types.StorageOutputs{}, fmt.Errorf("attach volume %s: %w", volName, err)
	}

	return types.StorageOutputs{DevicePath: vol.LinuxDevice}, nil
}
