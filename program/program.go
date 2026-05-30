// Command program is the Pulumi program that turns an environment's resolved
// resources into a deployment. It mirrors the TypeScript toolkit's index.ts,
// adapted to the improved model: bootstrap-free manifest assembly, service
// awareness, and per-region iteration.
//
// This phase ships it as a compiling stub. Real providers land in later PRs, so
// at preview every provider lookup returns "unknown provider" — which is the
// expected behaviour until the compute-provider PR wires real implementations.
package main

import (
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	"github.com/wardnet/inforge/internal/bootstrap"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/manifest"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/registry"
	"github.com/wardnet/inforge/internal/service"
	"github.com/wardnet/inforge/internal/tags"
	"github.com/wardnet/inforge/internal/types"
)

func main() {
	pulumi.Run(run)
}

func run(ctx *pulumi.Context) error {
	cfg := config.New(ctx, "")
	env := cfg.Require("environment")
	dir := "./resources"
	if d := cfg.Get("dir"); d != "" {
		dir = d
	}

	vars, err := loader.LoadVariables(env, dir)
	if err != nil {
		return err
	}
	regionTable, err := loader.LoadRegionTable(env, dir)
	if err != nil {
		return err
	}
	byRegion, err := loader.LoadResources(env, dir)
	if err != nil {
		return err
	}

	// The deploy descriptor is a pure function of resolved resources; surface it
	// as a stack output for the deployment workflow.
	desc, err := service.BuildDeployDescriptor(env, vars.BaseDomain, byRegion, regionTable)
	if err != nil {
		return err
	}
	ctx.Export("deployDescriptor", pulumi.Any(desc))

	registries := make(map[string]registry.ProviderRegistry, len(vars.Regions))
	for _, re := range vars.Regions {
		merged := registry.MergeProviders(vars.Providers, re.Providers)
		registries[re.Name] = registry.BuildRegistry(merged, vars.SSH)
	}

	networkOutputs := map[string]map[string]types.NetworkOutputs{}
	computeOutputs := map[string]map[string]types.ComputeOutputs{}
	databaseOutputs := map[string]map[string]types.DatabaseOutputs{}

	for _, re := range vars.Regions {
		region := re.Name
		reg := registries[region]
		res := byRegion[region]
		slug, err := regionTable.Slug(region)
		if err != nil {
			return err
		}
		networkOutputs[region] = map[string]types.NetworkOutputs{}
		computeOutputs[region] = map[string]types.ComputeOutputs{}
		databaseOutputs[region] = map[string]types.DatabaseOutputs{}

		for _, spec := range res.Network {
			np, err := reg.Network(spec.Provider)
			if err != nil {
				return err
			}
			out, err := np.Create(ctx, spec, env, region)
			if err != nil {
				return err
			}
			networkOutputs[region][naming.SpecKey(spec.Name, spec.Instance)] = out
		}

		for _, spec := range res.Compute {
			man, err := assembleManifest(reg, spec, res, env, region, slug)
			if err != nil {
				return err
			}
			cp, err := reg.Compute(spec.Provider)
			if err != nil {
				return err
			}
			for i := 1; i <= spec.InstanceCount; i++ {
				key := naming.SpecKey(spec.Name, i)
				domain := fmt.Sprintf("%s.%s.%s", subdomainFor(key, spec.Name, res.DNS), slug, vars.BaseDomain)
				out, err := cp.Create(ctx, spec, networkOutputs[region][spec.Network], env, region, domain, man.Manifest)
				if err != nil {
					return err
				}
				computeOutputs[region][key] = out
			}
		}

		for _, spec := range res.Database {
			dp, err := reg.Database(spec.Provider)
			if err != nil {
				return err
			}
			out, err := dp.Create(ctx, spec, env, region)
			if err != nil {
				return err
			}
			databaseOutputs[region][spec.Name] = out
		}

		for _, spec := range res.Secrets {
			sp, err := reg.Secrets(spec.Provider)
			if err != nil {
				return err
			}
			all := types.AllOutputs{Compute: computeOutputs, Database: databaseOutputs}
			if err := sp.Create(ctx, spec, env, region, all); err != nil {
				return err
			}
		}
	}

	for _, re := range vars.Regions {
		region := re.Name
		reg := registries[region]
		res := byRegion[region]
		slug, err := regionTable.Slug(region)
		if err != nil {
			return err
		}
		for _, spec := range res.DNS {
			dp, err := reg.DNS(spec.Provider)
			if err != nil {
				return err
			}
			recordName := fmt.Sprintf("%s.%s", spec.Subdomain, slug)
			if err := dp.Create(ctx, spec, computeOutputs[region][spec.Compute], recordName); err != nil {
				return err
			}
		}
	}

	return nil
}

// assembleManifest builds a compute's manifest from the registry's contributors.
// Secrets, if any, are encrypted to a freshly minted key K (the bootstrap
// trigger lives in the manifest data, not a flag).
func assembleManifest(reg registry.ProviderRegistry, spec types.ComputeSpec, res types.Resources, env, region, slug string) (manifest.Result, error) {
	base := manifest.Base{
		Version:   1,
		Region:    region,
		Namespace: tags.ContainerTag(slug, env, spec.Container),
	}
	var contributions []types.ManifestContribution
	for _, c := range reg.ManifestContributors() {
		contribution, err := c.ContributeToManifest(spec, res, env, region)
		if err != nil {
			return manifest.Result{}, err
		}
		contributions = append(contributions, contribution)
	}
	mat, err := bootstrap.Mint()
	if err != nil {
		return manifest.Result{}, err
	}
	return manifest.Generate(base, contributions, mat.Recipient)
}

// subdomainFor returns the DNS subdomain for a compute instance, falling back
// to the compute name when no DNS record targets it.
func subdomainFor(key, name string, dns []types.DnsSpec) string {
	for _, d := range dns {
		if d.Compute == key {
			return d.Subdomain
		}
	}
	return name
}
