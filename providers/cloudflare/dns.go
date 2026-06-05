// Package cloudflare provides the Cloudflare DNS provider implementation.
package cloudflare

import (
	cf "github.com/pulumi/pulumi-cloudflare/sdk/v6/go/cloudflare"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

// CloudflareDns creates Cloudflare A-records pointing at compute public IPs.
type CloudflareDns struct {
	zoneID   string
	project  string
	env      string
	slug     string
	// tagRecords controls whether inforge labels each DNS record with its
	// resource tags. Record tags are a Cloudflare Enterprise-only feature; on
	// other plans the API rejects them (error 9300, "exceeding the quota of 0"),
	// so projects on non-Enterprise zones must disable this.
	tagRecords bool
	provider   *cf.Provider
}

// New returns a CloudflareDns configured with the given zone ID, labeling
// metadata, and shared provider. tagRecords enables DNS record tagging, which
// requires a Cloudflare Enterprise zone.
func New(zoneID, project, env, slug string, tagRecords bool, provider *cf.Provider) *CloudflareDns {
	return &CloudflareDns{
		zoneID:     zoneID,
		project:    project,
		env:        env,
		slug:       slug,
		tagRecords: tagRecords,
		provider:   provider,
	}
}

// Create creates one Cloudflare A-record for the given DnsSpec.
func (c *CloudflareDns) Create(ctx *pulumi.Context, spec types.DnsSpec, compute types.ComputeOutputs) error {
	recordName := naming.RecordName(c.env, c.slug, spec.Subdomain)
	pulumiName := naming.Resource(c.env, c.slug, "record", spec.Name)

	ttl := pulumi.Float64(60)
	if spec.Proxied {
		ttl = pulumi.Float64(1) // Cloudflare requires ttl=1 when proxied
	}
	args := &cf.RecordArgs{
		ZoneId:  pulumi.StringPtr(c.zoneID),
		Name:    pulumi.String(recordName),
		Type:    pulumi.String("A"),
		Content: compute.PublicIP.ToStringPtrOutput(),
		Proxied: pulumi.BoolPtr(spec.Proxied),
		Ttl:     ttl,
	}
	// Record tags require a Cloudflare Enterprise zone; only set them when the
	// project opts in, otherwise the create is rejected on non-Enterprise plans.
	if c.tagRecords {
		cfTags := tags.CloudflareTags(c.project, c.env, c.slug, spec.Container)
		tagInputs := make(pulumi.StringArray, len(cfTags))
		for i, t := range cfTags {
			tagInputs[i] = pulumi.String(t)
		}
		args.Tags = tagInputs
	}
	_, err := cf.NewRecord(ctx, pulumiName, args, c.providerOpts()...)
	return err
}

// providerOpts returns the Pulumi provider resource option when a provider is
// set, and nil otherwise (used in tests where provider is nil).
func (c *CloudflareDns) providerOpts() []pulumi.ResourceOption {
	if c.provider == nil {
		return nil
	}
	return []pulumi.ResourceOption{pulumi.Provider(c.provider)}
}
