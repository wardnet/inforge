// Package cloudflare provides the Cloudflare DNS provider implementation.
package cloudflare

import (
	cf "github.com/pulumi/pulumi-cloudflare/sdk/v6/go/cloudflare"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

// CloudflareDns creates Cloudflare A-records pointing at compute public IPs.
type CloudflareDns struct {
	zoneID   string
	project  string
	env      string
	region   string
	provider *cf.Provider
}

// New returns a CloudflareDns configured with the given zone ID, labeling
// metadata, and shared provider.
func New(zoneID, project, env, region string, provider *cf.Provider) *CloudflareDns {
	return &CloudflareDns{
		zoneID:   zoneID,
		project:  project,
		env:      env,
		region:   region,
		provider: provider,
	}
}

// Create creates one Cloudflare A-record for the given DnsSpec. recordName is the
// full DNS record name (e.g. "api.euc1") and is used as the Pulumi resource
// logical name, ensuring uniqueness across multi-region deployments.
func (c *CloudflareDns) Create(ctx *pulumi.Context, spec types.DnsSpec, compute types.ComputeOutputs, recordName string) error {
	ttl := pulumi.Float64(60)
	if spec.Proxied {
		ttl = pulumi.Float64(1) // Cloudflare requires ttl=1 when proxied
	}
	cfTags := tags.CloudflareTags(c.project, c.env, c.region, spec.Container)
	tagInputs := make(pulumi.StringArray, len(cfTags))
	for i, t := range cfTags {
		tagInputs[i] = pulumi.String(t)
	}
	_, err := cf.NewRecord(ctx, recordName, &cf.RecordArgs{
		ZoneId:  pulumi.StringPtr(c.zoneID),
		Name:    pulumi.String(recordName),
		Type:    pulumi.String("A"),
		Content: compute.PublicIP.ToStringPtrOutput(),
		Proxied: pulumi.BoolPtr(spec.Proxied),
		Ttl:     ttl,
		Tags:    tagInputs,
	}, pulumi.Provider(c.provider))
	return err
}
