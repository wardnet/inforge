// Package program is the Pulumi program that turns an environment's resolved
// resources into a deployment. It is used as an inline program via the
// Automation API in the inforge CLI.
package program

import (
	"fmt"
	"os"
	"strconv"

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

// Run is the Pulumi program entry point, passed to the Automation API as an
// inline program source.
func Run(ctx *pulumi.Context) error {
	cfg := config.New(ctx, "")
	env := cfg.Require("environment")
	dir := "./resources"
	if d := cfg.Get("dir"); d != "" {
		dir = d
	}

	// Escrow config — required only when a manifest has secret values.
	// oidc_token falls back to INFORGE_OIDC_TOKEN so CI workflows can inject it
	// without writing a short-lived value into the stack config file.
	// tenant defaults to GITHUB_REPOSITORY (set automatically by GitHub Actions).
	// broker_ttl_seconds falls back to INFORGE_BROKER_TTL_SECONDS; default 600.
	// Set it to a short value (e.g. 60) in preview jobs so the throwaway key
	// expires quickly.
	brokerURL := cfg.Get("broker_url")
	oidcToken := cfg.Get("oidc_token")
	if oidcToken == "" {
		oidcToken = os.Getenv("INFORGE_OIDC_TOKEN")
	}
	tenant := cfg.Get("tenant")
	if tenant == "" {
		tenant = os.Getenv("GITHUB_REPOSITORY")
	}
	brokerTTL := 600
	if v := cfg.Get("broker_ttl_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			brokerTTL = n
		}
	}
	if v := os.Getenv("INFORGE_BROKER_TTL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			brokerTTL = n
		}
	}

	var brokerClient bootstrap.KeyBrokerClient
	if brokerURL != "" && oidcToken != "" {
		brokerClient = bootstrap.NewHTTPKeyBrokerClient(brokerURL, oidcToken, nil)
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

	desc, err := service.BuildDeployDescriptor(env, vars.BaseDomain, byRegion, regionTable)
	if err != nil {
		return err
	}
	ctx.Export("deployDescriptor", pulumi.Any(desc))

	registries := make(map[string]registry.ProviderRegistry, len(vars.Regions))
	for _, re := range vars.Regions {
		merged := registry.MergeProviders(vars.Providers, re.Providers)
		registries[re.Name] = registry.BuildRegistry(ctx, merged, vars.SSH, regionTable, ctx.Project(), env, re.Name)
	}

	// networkOutputs: region → specName+"/"+subnetName → NetworkOutputs
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
			subnetMap, err := np.Create(ctx, spec, env, region)
			if err != nil {
				return err
			}
			for subnetName, out := range subnetMap {
				networkOutputs[region][spec.Name+"/"+subnetName] = out
			}
		}

		for _, spec := range res.Compute {
			man, mat, err := assembleManifest(reg, spec, res, env, region, slug)
			if err != nil {
				return err
			}

			var bootstrapDoc string
			if man.BootstrapNeeded {
				if brokerClient == nil {
					return fmt.Errorf("compute %s/%s has secret values but key broker is not configured (set broker_url and oidc_token)", region, spec.Name)
				}
				doc, regErr := bootstrap.Register(brokerClient, brokerURL, tenant, mat, brokerTTL)
				if regErr != nil {
					return fmt.Errorf("register bootstrap key for %s/%s: %w", region, spec.Name, regErr)
				}
				docBytes, marshalErr := doc.Marshal()
				if marshalErr != nil {
					return fmt.Errorf("marshal bootstrap doc for %s/%s: %w", region, spec.Name, marshalErr)
				}
				bootstrapDoc = string(docBytes)
			}

			netOut, err := resolveNetworkOutput(spec, res.Network, networkOutputs[region])
			if err != nil {
				return fmt.Errorf("compute %s/%s: %w", region, spec.Name, err)
			}

			cp, err := reg.Compute(spec.Provider)
			if err != nil {
				return err
			}
			for i := 1; i <= spec.InstanceCount; i++ {
				key := naming.SpecKey(spec.Name, i)
				domain := fmt.Sprintf("%s.%s.%s", subdomainFor(key, spec.Name, res.DNS), slug, vars.BaseDomain)
				out, err := cp.Create(ctx, spec, netOut, env, region, domain, man.Manifest, bootstrapDoc)
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
		for _, spec := range res.DNS {
			dp, err := reg.DNS(spec.Provider)
			if err != nil {
				return err
			}
			if err := dp.Create(ctx, spec, computeOutputs[region][spec.Compute]); err != nil {
				return err
			}
		}
	}

	return nil
}

// resolveNetworkOutput returns the NetworkOutputs for the subnet a compute spec
// should attach to. If spec.Subnet is set, it is used as the subnet name;
// otherwise the first subnet of the network spec (by declaration order) is
// used, which is deterministic.
func resolveNetworkOutput(spec types.ComputeSpec, networks []types.NetworkSpec, netOuts map[string]types.NetworkOutputs) (types.NetworkOutputs, error) {
	// Find the network spec by name.
	var netSpec *types.NetworkSpec
	for i := range networks {
		if networks[i].Name == spec.Network {
			netSpec = &networks[i]
			break
		}
	}
	if netSpec == nil {
		return types.NetworkOutputs{}, fmt.Errorf("network %q not found", spec.Network)
	}

	subnetName := spec.Subnet
	if subnetName == "" {
		if len(netSpec.Subnets) == 0 {
			return types.NetworkOutputs{}, fmt.Errorf("network %q has no subnets", spec.Network)
		}
		subnetName = netSpec.Subnets[0].Name
	}

	key := spec.Network + "/" + subnetName
	out, ok := netOuts[key]
	if !ok {
		return types.NetworkOutputs{}, fmt.Errorf("network output %q not found", key)
	}
	return out, nil
}

func assembleManifest(reg registry.ProviderRegistry, spec types.ComputeSpec, res types.Resources, env, region, slug string) (manifest.Result, bootstrap.Material, error) {
	base := manifest.Base{
		Version:   1,
		Region:    region,
		Namespace: tags.ContainerTag(slug, env, spec.Container),
	}
	var contributions []types.ManifestContribution
	for _, c := range reg.ManifestContributors() {
		contribution, err := c.ContributeToManifest(spec, res, env, region)
		if err != nil {
			return manifest.Result{}, bootstrap.Material{}, err
		}
		contributions = append(contributions, contribution)
	}

	// Probe without a real recipient: if there are no secret values, Generate
	// returns early without touching the recipient, saving a Mint() call.
	probe, err := manifest.Generate(base, contributions, "")
	if err != nil {
		return manifest.Result{}, bootstrap.Material{}, err
	}
	if !probe.BootstrapNeeded {
		return probe, bootstrap.Material{}, nil
	}

	// Secrets are present: mint a fresh age key and re-generate with it.
	mat, err := bootstrap.Mint()
	if err != nil {
		return manifest.Result{}, bootstrap.Material{}, err
	}
	result, err := manifest.Generate(base, contributions, mat.Recipient)
	return result, mat, err
}

func subdomainFor(key, name string, dns []types.DnsSpec) string {
	for _, d := range dns {
		if d.Compute == key {
			return d.Subdomain
		}
	}
	return name
}
