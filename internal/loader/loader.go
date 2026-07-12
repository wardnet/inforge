// Package loader reads a project's on-disk resource definitions for a single
// environment: variables.yaml, the optional per-env region/size tables, and
// the folder-based resource set under resources/<env>/regional/<type>/<name>/
// manifest.yaml (plus sidecars), instantiated into every region. It applies the
// defaults that yaml.v3 cannot, loads each service's environment.yaml sidecar,
// and resolves cloud-init paths. Cross-resource and semantic validation lives in
// internal/validate.
//
// Loading never expands the ${ENV_VAR} references variables.yaml and regions.yaml
// carry: the two loaders return RAW documents (RawVariables, RawRegions) with the
// placeholders intact, and expansion is an explicit second step through a Resolver
// (see resolve.go). A command therefore requires exactly the variables it reads
// and no others — the deploy program resolves the whole document up front and
// fails before it touches a cloud, while a leaf mint resolves base_domain alone.
package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wardnet/inforge/internal/postgres"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/sizes"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// envDir returns the directory holding one environment's definitions.
func envDir(env, dir string) string {
	return filepath.Join(dir, env)
}

// LoadVariablesRaw reads and parses <dir>/<env>/variables.yaml WITHOUT expanding
// its ${ENV_VAR} references — the placeholders survive into RawVariables, and no
// environment variable is required to load the file.
//
// Expansion is a separate, explicit step: pass the result to a Resolver and ask
// for the fields you need (Resolver.Variables for the whole document, or
// Resolver.String for one field). This is what lets a command require only the
// variables it actually reads — `inforge pki renew` and `inforge releases deploy`
// resolve base_domain and nothing else, so an unset SSH_AUTHORIZED_KEYS no longer
// fails a leaf mint that never looks at an SSH key.
func LoadVariablesRaw(env, dir string) (RawVariables, error) {
	var vars RawVariables
	path := filepath.Join(envDir(env, dir), "variables.yaml")
	b, err := os.ReadFile(path) // #nosec G304 -- path derives from operator-supplied --dir/env
	if err != nil {
		return vars, fmt.Errorf("read variables: %w", err)
	}
	if err := yaml.Unmarshal(b, &vars); err != nil {
		return vars, fmt.Errorf("decode variables: %w", err)
	}
	return vars, nil
}

// LoadRegionsRaw parses an environment's resources/<env>/regions.yaml — the
// per-region table (slug + provider config) under the top-level `regions:` key,
// plus the optional region-less `global:` block — WITHOUT expanding its
// ${ENV_VAR} references. regions.yaml is the single authority for which regions
// deploy and all provider config. A missing file yields an empty table and nil
// global.
//
// As with LoadVariablesRaw, expansion is the Resolver's job. The raw table is a
// distinct type from regions.Table precisely so an unexpanded
// `apiToken: ${HCLOUD_TOKEN}` cannot be handed to a provider: the registry takes
// a resolved regions.Table, and the compiler rejects the raw one. Structural
// validation reads the raw form directly — it checks the region/provider shape
// without needing, or being tripped by the absence of, real credentials.
func LoadRegionsRaw(env, dir string) (RawRegions, error) {
	path := filepath.Join(envDir(env, dir), "regions.yaml")
	b, err := os.ReadFile(path) // #nosec G304 -- path derives from operator-supplied --dir/env
	if os.IsNotExist(err) {
		return RawRegions{Table: RawTable{}}, nil
	}
	if err != nil {
		return RawRegions{}, fmt.Errorf("read regions table: %w", err)
	}
	var f regions.File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return RawRegions{}, fmt.Errorf("decode regions table: %w", err)
	}
	out := RawRegions{Table: RawTable{}}
	for name, ar := range f.Regions {
		out.Table[name] = ar
	}
	if f.Global != nil {
		g := RawGlobal(*f.Global)
		out.Global = &g
	}
	return out, nil
}

// LoadSizeTable returns the size table for an environment: the per-env
// resources/<env>/sizes.yaml if present (replacing the defaults wholesale),
// otherwise the built-in defaults. The file is a YAML list of size names, e.g.
// `[SMALL, MEDIUM, LARGE]` — the size table is cloud-agnostic vocabulary with no
// cpus/memory payload (a provider maps a size name to a concrete SKU).
func LoadSizeTable(env, dir string) (sizes.Table, error) {
	path := filepath.Join(envDir(env, dir), "sizes.yaml")
	b, err := os.ReadFile(path) // #nosec G304 -- path derives from operator-supplied --dir/env
	if os.IsNotExist(err) {
		return sizes.DefaultTable(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sizes table: %w", err)
	}
	var names []string
	if err := yaml.Unmarshal(b, &names); err != nil {
		return nil, fmt.Errorf("parse sizes table: %w", err)
	}
	tbl := make(sizes.Table, len(names))
	for _, n := range names {
		tbl[n] = struct{}{}
	}
	return tbl, nil
}

// loadTypeFromFolders reads every named sub-folder under dir: each folder must
// contain a manifest.yaml that decodes into T. A missing directory yields no
// resources (same as the old loadType). A directory entry without a
// manifest.yaml is an error (catches typos). Non-directory entries are ignored.
// The parallel folders slice carries the absolute folder path for each spec,
// letting callers resolve per-resource sidecars (cloud-init.sh, environment.yaml).
func loadTypeFromFolders[T any](dir string) ([]T, []string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var specs []T
	var folders []string
	var missing []string
	for _, e := range entries {
		if !e.IsDir() {
			continue // ignore stray files at the type level
		}
		folder := filepath.Join(dir, e.Name())
		manifest := filepath.Join(folder, "manifest.yaml")
		b, err := os.ReadFile(manifest) // #nosec G304 -- folder derives from operator-supplied --dir tree
		if os.IsNotExist(err) {
			// Accumulate all missing-manifest errors so the caller sees every
			// broken folder at once, not just the first one.
			missing = append(missing, fmt.Sprintf("resource folder %q has no manifest.yaml", folder))
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", manifest, err)
		}
		var v T
		if err := yaml.Unmarshal(b, &v); err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", manifest, err)
		}
		specs = append(specs, v)
		folders = append(folders, folder)
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("%s", strings.Join(missing, "\n"))
	}
	return specs, folders, nil
}

// LoadEnvironmentFile reads the environment.yaml sidecar from a service's
// folder. Returns nil, nil when the file is absent (a service may have no env
// contract). Returns an error on malformed YAML.
func LoadEnvironmentFile(folder string) (map[string]string, error) {
	path := filepath.Join(folder, "environment.yaml")
	b, err := os.ReadFile(path) // #nosec G304 -- folder derives from operator-supplied --dir tree
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m map[string]string
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

// LoadResources reads the single shared resource set under
// <dir>/<env>/regional/{network,compute,…} and returns it parsed and
// default-normalised. The set is defined once and instantiated into every region
// from regions.yaml (the region slug in each cloud name keeps instances unique
// per region), so there is no per-region directory to walk. cloud_init paths are
// resolved to absolute paths relative to the compute dir.
func LoadResources(env, dir string) (types.Resources, error) {
	return loadResourceSet(filepath.Join(envDir(env, dir), "regional"))
}

// LoadGlobalResources reads the global resource set under
// <dir>/<env>/global/{network,compute,…} and returns it parsed and
// default-normalised. The global set is region-less: it is instantiated once
// (not per region) with region-less naming, and other regions may reference its
// outputs under strict cross-reference rules (see internal/validate). A missing
// global/ directory yields an empty set (loadType treats absent dirs as empty),
// so the global slice is optional. The regional LoadResources walks named type
// dirs (network, compute, …) and never lists base children, so it does not pick
// up global/ as a regional resource type.
func LoadGlobalResources(env, dir string) (types.Resources, error) {
	return loadResourceSet(filepath.Join(envDir(env, dir), "global"))
}

// loadResourceSet reads every resource type directory under base into a single
// types.Resources, applying the defaults yaml.v3 cannot and resolving cloud_init
// paths relative to base/compute. It backs both the regional and global loaders,
// which differ only in their base directory.
func loadResourceSet(base string) (types.Resources, error) {
	var res types.Resources

	netSpecs, _, err := loadTypeFromFolders[types.NetworkSpec](filepath.Join(base, "network"))
	if err != nil {
		return types.Resources{}, err
	}
	for i := range netSpecs {
		NormalizeNetwork(&netSpecs[i])
	}
	res.Network = netSpecs

	computeSpecs, computeFolders, err := loadTypeFromFolders[types.ComputeSpec](filepath.Join(base, "compute"))
	if err != nil {
		return types.Resources{}, err
	}
	for i := range computeSpecs {
		NormalizeCompute(&computeSpecs[i], computeFolders[i])
	}
	res.Compute = computeSpecs

	clusterSpecs, _, err := loadTypeFromFolders[types.DatabaseClusterSpec](filepath.Join(base, "database-cluster"))
	if err != nil {
		return types.Resources{}, err
	}
	for i := range clusterSpecs {
		NormalizeDatabaseCluster(&clusterSpecs[i])
	}
	res.DatabaseCluster = clusterSpecs

	dbSpecs, _, err := loadTypeFromFolders[types.DatabaseSpec](filepath.Join(base, "database"))
	if err != nil {
		return types.Resources{}, err
	}
	for i := range dbSpecs {
		NormalizeDatabase(&dbSpecs[i])
	}
	res.Database = dbSpecs

	svcSpecs, svcFolders, err := loadTypeFromFolders[types.ServiceSpec](filepath.Join(base, "service"))
	if err != nil {
		return types.Resources{}, err
	}
	for i := range svcSpecs {
		env, err := LoadEnvironmentFile(svcFolders[i])
		if err != nil {
			return types.Resources{}, err
		}
		svcSpecs[i].Environment = env
		NormalizeService(&svcSpecs[i])
	}
	res.Service = svcSpecs

	pkiSpecs, _, err := loadTypeFromFolders[types.PKIResourceSpec](filepath.Join(base, "pki"))
	if err != nil {
		return types.Resources{}, err
	}
	for i := range pkiSpecs {
		NormalizePKIResource(&pkiSpecs[i])
	}
	res.PKI = pkiSpecs

	ingressSpecs, _, err := loadTypeFromFolders[types.IngressSpec](filepath.Join(base, "ingress"))
	if err != nil {
		return types.Resources{}, err
	}
	for i := range ingressSpecs {
		NormalizeIngress(&ingressSpecs[i])
	}
	res.Ingress = ingressSpecs

	appSpecs, _, err := loadTypeFromFolders[types.AppSpec](filepath.Join(base, "app"))
	if err != nil {
		return types.Resources{}, err
	}
	for i := range appSpecs {
		NormalizeApp(&appSpecs[i])
	}
	res.App = appSpecs

	gatewaySpecs, _, err := loadTypeFromFolders[types.GatewaySpec](filepath.Join(base, "gateway"))
	if err != nil {
		return types.Resources{}, err
	}
	for i := range gatewaySpecs {
		NormalizeGateway(&gatewaySpecs[i])
	}
	res.Gateway = gatewaySpecs

	return res, nil
}

// NormalizeNetwork is a no-op placeholder retained for the applyDefaults call.
func NormalizeNetwork(_ *types.NetworkSpec) {}

// NormalizeCompute applies compute defaults (kind=vm, instance_count=1) and
// resolves a relative cloud_init path against computeDir.
func NormalizeCompute(c *types.ComputeSpec, computeDir string) {
	if c.Kind == "" {
		c.Kind = "vm"
	}
	if c.InstanceCount == 0 {
		c.InstanceCount = 1
	}
	if c.CloudInit != "" && !filepath.IsAbs(c.CloudInit) {
		joined := filepath.Join(computeDir, c.CloudInit)
		if abs, err := filepath.Abs(joined); err == nil {
			c.CloudInit = abs
		} else {
			c.CloudInit = joined
		}
	}
}

// NormalizeDatabaseCluster trims the cluster's free-text and FK fields and defaults
// the engine version to postgres.DefaultVersion when omitted (ADR-0036). There is no
// default provider to apply here — ResolveProvider supplies "self-hosted" when the
// spec and inforge.yaml name none.
func NormalizeDatabaseCluster(d *types.DatabaseClusterSpec) {
	d.Name = strings.TrimSpace(d.Name)
	d.Container = strings.TrimSpace(d.Container)
	d.Engine = strings.TrimSpace(d.Engine)
	d.Host = strings.TrimSpace(d.Host)
	d.Provider = strings.TrimSpace(d.Provider)
	d.Version = strings.TrimSpace(d.Version)
	if d.Version == "" {
		d.Version = postgres.DefaultVersion
	}
}

// NormalizeDatabase trims the logical database's free-text and FK fields and applies
// the backup-policy defaults (ADR-0036): backups default to enabled (an absent block
// or absent enabled → true; an explicit `enabled: false` opts out), a 24h interval,
// and keep 7.
func NormalizeDatabase(d *types.DatabaseSpec) {
	d.Name = strings.TrimSpace(d.Name)
	d.Container = strings.TrimSpace(d.Container)
	d.Cluster = strings.TrimSpace(d.Cluster)
	d.Database = strings.TrimSpace(d.Database)
	d.Owner = strings.TrimSpace(d.Owner)
	if d.Backup.Enabled == nil {
		enabled := true
		d.Backup.Enabled = &enabled
	}
	if d.Backup.Interval == "" {
		d.Backup.Interval = "24h"
	}
	if d.Backup.Keep == 0 {
		d.Backup.Keep = 7
	}
}

// NormalizeService applies service defaults (type defaults to "raw") and trims the
// ingress foreign key so a stray space cannot defeat its resolution.
func NormalizeService(s *types.ServiceSpec) {
	if s.Type == "" {
		s.Type = "raw"
	}
	s.Pki = strings.TrimSpace(s.Pki)
	s.Reload = strings.TrimSpace(s.Reload)
	s.Ingress = strings.TrimSpace(s.Ingress)
	trimAll(s.HealthProbePaths)
	// Trim the bare service names in the mesh allow list and the path globs so a
	// stray space cannot defeat resolution (ADR-0032/0034).
	if s.Mesh != nil {
		trimAll(s.Mesh.AllowedServices)
		trimAll(s.Mesh.PublicPaths)
		trimAll(s.Mesh.InternalPaths)
	}
}

// trimAll trims every element of a string slice in place.
func trimAll(ss []string) {
	for i := range ss {
		ss[i] = strings.TrimSpace(ss[i])
	}
}

// NormalizeGateway trims the gateway spec's free-text fields, its host foreign
// key, the listed service FKs, and its health probe paths, and normalizes the
// public health port to its effective (defaulted) value — so a stray space
// cannot defeat resolution. Like ingress it has no default provider to apply —
// the provider comes from the compute host the gateway references, not the spec.
func NormalizeGateway(g *types.GatewaySpec) {
	g.Name = strings.TrimSpace(g.Name)
	g.Container = strings.TrimSpace(g.Container)
	g.Host = strings.TrimSpace(g.Host)
	g.Pki = strings.TrimSpace(g.Pki)
	g.Subdomain = strings.TrimSpace(g.Subdomain)
	trimAll(g.Services)
	trimAll(g.HealthProbePaths)
	g.HealthProbesPort = g.EffectiveHealthProbesPort()
}

// NormalizeIngress trims the ingress spec's free-text fields and its host
// foreign key so a stray space cannot defeat the host FK resolution. There is no
// default provider to apply — the provider comes from the compute host the
// ingress references, not the spec.
func NormalizeIngress(i *types.IngressSpec) {
	i.Name = strings.TrimSpace(i.Name)
	i.Container = strings.TrimSpace(i.Container)
	i.Host = strings.TrimSpace(i.Host)
	i.HealthProbesPort = i.EffectiveHealthProbesPort()
}

// NormalizeApp trims the app spec's foreign-key and domain fields so a stray
// space cannot defeat the ingress FK resolution or the subdomain composition.
// spa defaults to false (no SPA fallback) when omitted.
func NormalizeApp(a *types.AppSpec) {
	a.Name = strings.TrimSpace(a.Name)
	a.Container = strings.TrimSpace(a.Container)
	a.Ingress = strings.TrimSpace(a.Ingress)
	a.Subdomain = strings.TrimSpace(a.Subdomain)
}

// NormalizePKIResource trims the PKI resource's topology so a stray space does
// not defeat the root-only check. There is no default topology — a PKI resource
// must declare it explicitly (validated against the schema and semantically).
func NormalizePKIResource(p *types.PKIResourceSpec) {
	p.Topology = strings.TrimSpace(p.Topology)
}
