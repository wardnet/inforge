// Package validate checks a project's resource definitions for one environment:
// each file against its embedded JSON Schema, plus the semantic and
// cross-resource rules that a schema cannot express (CIDR hierarchy, foreign
// keys against expanded compute specKeys, the secrets source DSL, provider
// availability). It prints an OK/FAIL line per file and returns an error if any
// file failed.
package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/wardnet/inforge/internal/bootstrapper"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/sizes"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/schemas"
	"gopkg.in/yaml.v3"
)

// reporter accumulates pass/fail state while printing per-file results.
type reporter struct {
	failed bool
}

func (r *reporter) report(path string, errs, warns []string) {
	for _, w := range warns {
		fmt.Printf("WARN %s\n     %s\n", path, w)
	}
	if len(errs) > 0 {
		r.failed = true
		fmt.Printf("FAIL %s\n", path)
		for _, e := range errs {
			fmt.Printf("     %s\n", e)
		}
		return
	}
	fmt.Printf("OK   %s\n", path)
}

// fail records a standalone failure not tied to a resource file.
func (r *reporter) fail(label string, msgs ...string) {
	r.failed = true
	fmt.Printf("FAIL %s\n", label)
	for _, m := range msgs {
		fmt.Printf("     %s\n", m)
	}
}

// fileOf is a resource file read both as a raw document (for schema validation)
// and as a typed, default-normalised spec (for semantic checks).
type fileOf[T any] struct {
	path     string
	raw      any
	spec     T
	parseErr error
}

func readFiles[T any](dir string) ([]fileOf[T], error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []fileOf[T]
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		f := fileOf[T]{path: path}
		if err := yaml.Unmarshal(b, &f.raw); err != nil {
			f.parseErr = err
			out = append(out, f)
			continue
		}
		if err := yaml.Unmarshal(b, &f.spec); err != nil {
			f.parseErr = err
		}
		out = append(out, f)
	}
	return out, nil
}

// globalRefs carries the global slice's referenceable outputs so a regional
// secrets `ref:` may resolve a `global/<name>` target. It holds the global
// database names and expanded compute specKeys; the regional validation context
// is seeded with these under a `global/` prefix (see validateResourceSet). It is
// nil for the global slice's own validation pass, which runs in a global-only
// context so that a global resource referencing a regional one fails as
// not-found (enforcing "global → global only").
type globalRefs struct {
	databaseNames map[string]bool   // bare global database name -> true
	computeKind   map[string]string // accepted global compute FK form -> kind
}

// buildGlobalRefs derives the cross-referenceable outputs from the loaded
// global resource set (already default-normalised by the loader). Single-instance
// computes are additionally keyed by their bare name, mirroring CanonicalComputeKeys.
func buildGlobalRefs(global types.Resources) *globalRefs {
	g := &globalRefs{databaseNames: map[string]bool{}, computeKind: map[string]string{}}
	for _, d := range global.Database {
		g.databaseNames[d.Name] = true
	}
	for _, c := range global.Compute {
		for i := 1; i <= c.InstanceCount; i++ {
			g.computeKind[naming.SpecKey(c.Name, i)] = c.Kind
		}
		if c.InstanceCount == 1 {
			g.computeKind[c.Name] = c.Kind
		}
	}
	return g
}

// globalHasResources reports whether the global slice declares any resource.
func globalHasResources(g types.Resources) bool {
	return len(g.Network)+len(g.Compute)+len(g.Database)+
		len(g.Service) > 0
}

// regionContext holds the foreign-key targets and tables a region's semantic
// checks resolve against.
type regionContext struct {
	available        map[string]bool
	sizeTable        sizes.Table
	networks         map[string]types.NetworkSpec // specKey -> network
	computeKind      map[string]string            // expanded specKey -> kind
	computeCanonical map[string]string            // any accepted compute FK form -> canonical specKey
	computeInstances map[string]int               // canonical specKey -> the compute's instance_count
	computeDeployer  map[string]bool              // canonical specKey -> declares a deploy_user
	computeNames     map[string]bool              // bare compute names; service.host must be one of these
	databaseNames    map[string]bool
	// portUsersByHost maps a canonical host specKey to, per listen port, the names
	// of the services with an ingress entry on that port. It enforces that a
	// forward port is single-service-exclusive (and, since forward is the only
	// non-terminating type, that any shared port is tls-termination).
	portUsersByHost map[string]map[int][]string
	// targetUsersByHost maps a canonical host specKey to, per loopback target port,
	// the names of the services binding it. nginx forwards to 127.0.0.1:<target>, so
	// a target must belong to a single service and must not collide with any public
	// listen port nginx holds on the host (see checkService).
	targetUsersByHost map[string]map[int][]string
	// tlsTermIngressByHost marks a canonical host specKey that has at least one
	// tls-termination ingress entry across its services — so a forward on :80
	// (which ACME needs) can be rejected.
	tlsTermIngressByHost map[string]bool
	// encStore is the environment's committed encrypted secret store
	// (resources/<env>/secrets.enc.yaml), nil when the file does not exist. A
	// `vault:<KEY>` secret on a service must have a ciphertext under
	// (container, KEY); the check is presence-only so validation stays
	// credential-free.
	encStore *secretstore.Store
	// providerDefaults are the project-level provider fallbacks applied when a
	// spec omits its provider field (resolved via types.ResolveProvider).
	providerDefaults types.ProviderDefaults
}

// ValidateResources validates the single shared resource set under <dir>/<env>/
// and returns an error if any file failed. The resource set is defined once and
// instantiated into every region in regions.yaml, so its schema and foreign-key
// graph are region-independent and checked once; provider availability is then
// checked per region (a resource's provider must be declared in every region the
// set deploys into).
func ValidateResources(env, dir string, defaults types.ProviderDefaults) error {
	schemaSet, err := compileSchemas()
	if err != nil {
		return fmt.Errorf("compile schemas: %w", err)
	}

	// Structural validation must run without credentials, so load the region table
	// raw — ${ENV_VAR} references are left as literals rather than substituted:
	// validation checks the region/provider structure, not credential values, and
	// must not be tripped by an unset credential (e.g. an unresolved zone ref must
	// not read as an empty, "missing", zone).
	regionTable, global, err := loader.LoadRegionTableRaw(env, dir)
	if err != nil {
		return err
	}
	sizeTable, err := loader.LoadSizeTable(env, dir)
	if err != nil {
		return err
	}
	vars, err := loader.LoadVariablesLenient(env, dir)
	if err != nil {
		return err
	}
	// The global slice's referenceable outputs seed the regional context so a
	// regional secrets `ref:` may resolve a global/<name> database/compute target.
	globalRes, err := loader.LoadGlobalResources(env, dir)
	if err != nil {
		return err
	}

	r := &reporter{}
	base := filepath.Join(dir, env)
	// The encrypted secret store is optional (absent until `inforge secret init`);
	// a present-but-broken store is reported against its own path so the rest of
	// the resource set still validates. One env-scoped store serves both slices —
	// secrets are container-keyed, not region-keyed.
	encStore, err := secretstore.Load(secretstore.Path(dir, env))
	if err != nil && !errors.Is(err, secretstore.ErrNotFound) {
		r.fail(secretstore.Path(dir, env), err.Error())
		encStore = nil
	}
	globalBase := filepath.Join(base, "global")
	checkVariables(r, vars, filepath.Join(base, "variables.yaml"))
	checkRegionsFile(r, regionTable, global, filepath.Join(base, "regions.yaml"))

	// Validate the global slice in a GLOBAL-ONLY context (globalRefs nil): its FK
	// graph resolves only against global resources, so a global resource
	// referencing a regional one fails as not-found — enforcing "global → global
	// only". A global slice with resources but no global providers block is an error.
	if err := validateResourceSet(r, schemaSet, globalBase, nil, sizeTable, nil, encStore, defaults); err != nil {
		return err
	}
	if global == nil && globalHasResources(globalRes) {
		r.fail("regions.yaml [global]", "resources/"+env+"/global declares resources but regions.yaml has no global providers block")
	}

	// Validate the shared regional set once: schema + the region-independent FK
	// graph, with the global outputs injected so a regional secrets `ref:` may
	// resolve a global/<name> target. Provider availability is region-specific, so
	// it is skipped here (available nil) and checked separately per region below.
	if err := validateResourceSet(r, schemaSet, base, nil, sizeTable, buildGlobalRefs(globalRes), encStore, defaults); err != nil {
		return err
	}

	// Per-region provider availability: the same set deploys into every region, so
	// each resource's provider must be declared in that region's providers block.
	if err := checkProviderAvailability(r, base, regionTable, defaults); err != nil {
		return err
	}
	// The global slice realizes against the regions.yaml global providers block.
	if global != nil {
		if err := checkGlobalProviderAvailability(r, globalBase, global, defaults); err != nil {
			return err
		}
	}

	if r.failed {
		return errors.New("validation failed")
	}
	return nil
}

func validateResourceSet(r *reporter, schemaSet map[string]*jsonschema.Schema, base string, available map[string]bool, sizeTable sizes.Table, global *globalRefs, encStore *secretstore.Store, defaults types.ProviderDefaults) error {
	networkFiles, err := readFiles[types.NetworkSpec](filepath.Join(base, "network"))
	if err != nil {
		return err
	}
	computeDir := filepath.Join(base, "compute")
	computeFiles, err := readFiles[types.ComputeSpec](computeDir)
	if err != nil {
		return err
	}
	databaseFiles, err := readFiles[types.DatabaseSpec](filepath.Join(base, "database"))
	if err != nil {
		return err
	}
	serviceFiles, err := readFiles[types.ServiceSpec](filepath.Join(base, "service"))
	if err != nil {
		return err
	}

	// Apply defaults so semantic checks see normalised specs.
	for i := range networkFiles {
		loader.NormalizeNetwork(&networkFiles[i].spec)
	}
	for i := range computeFiles {
		loader.NormalizeCompute(&computeFiles[i].spec, computeDir)
	}
	for i := range databaseFiles {
		loader.NormalizeDatabase(&databaseFiles[i].spec)
	}
	for i := range serviceFiles {
		loader.NormalizeService(&serviceFiles[i].spec)
	}

	ctx := regionContext{
		available:            available,
		sizeTable:            sizeTable,
		networks:             map[string]types.NetworkSpec{},
		computeKind:          map[string]string{},
		computeCanonical:     map[string]string{},
		computeInstances:     map[string]int{},
		computeDeployer:      map[string]bool{},
		computeNames:         map[string]bool{},
		databaseNames:        map[string]bool{},
		portUsersByHost:      map[string]map[int][]string{},
		targetUsersByHost:    map[string]map[int][]string{},
		tlsTermIngressByHost: map[string]bool{},
		encStore:             encStore,
		providerDefaults:     defaults,
	}
	for _, f := range networkFiles {
		ctx.networks[f.spec.Name] = f.spec
	}
	computeSpecs := make([]types.ComputeSpec, 0, len(computeFiles))
	for _, f := range computeFiles {
		computeSpecs = append(computeSpecs, f.spec)
		hasDeployer := f.spec.DeployUser != nil && f.spec.DeployUser.Name != ""
		for i := 1; i <= f.spec.InstanceCount; i++ {
			key := naming.SpecKey(f.spec.Name, i)
			ctx.computeKind[key] = f.spec.Kind
			ctx.computeInstances[key] = f.spec.InstanceCount
			ctx.computeDeployer[key] = hasDeployer
		}
		ctx.computeNames[f.spec.Name] = true
		if f.spec.InstanceCount == 1 {
			// bridge and bridge-01 both reference the same host.
			ctx.computeKind[f.spec.Name] = f.spec.Kind
			ctx.computeInstances[f.spec.Name] = f.spec.InstanceCount
		}
	}
	// Canonicalization (any compute FK form -> expanded specKey) is shared with
	// the program so validation and realization agree on host identity.
	ctx.computeCanonical = naming.CanonicalComputeKeys(computeSpecs)
	for _, f := range databaseFiles {
		ctx.databaseNames[f.spec.Name] = true
	}
	// Aggregate per-host ingress so checkService can enforce the cross-service
	// rules: which services use each listen port (forward ports are
	// single-service-exclusive) and whether the host has any tls-termination entry
	// (so a forward on :80, which ACME owns, can be rejected).
	for _, f := range serviceFiles {
		c, ok := ctx.computeCanonical[f.spec.Host]
		if !ok {
			continue
		}
		for _, in := range f.spec.Ingress {
			if ctx.portUsersByHost[c] == nil {
				ctx.portUsersByHost[c] = map[int][]string{}
			}
			if ctx.targetUsersByHost[c] == nil {
				ctx.targetUsersByHost[c] = map[int][]string{}
			}
			ctx.portUsersByHost[c][in.Listen] = append(ctx.portUsersByHost[c][in.Listen], f.spec.Name)
			ctx.targetUsersByHost[c][in.Target] = append(ctx.targetUsersByHost[c][in.Target], f.spec.Name)
			if in.Type == types.IngressTypeTLSTermination {
				ctx.tlsTermIngressByHost[c] = true
			}
		}
	}
	// Seed the global slice's referenceable outputs under a `global/` prefix so a
	// regional secrets `ref:database/global/<name>` (RefName == "global/<name>")
	// resolves. Only database/compute outputs are referenceable cross-region;
	// service.host and compute.network to global are rejected explicitly below.
	if global != nil {
		for name := range global.databaseNames {
			ctx.databaseNames["global/"+name] = true
		}
		for key, kind := range global.computeKind {
			ctx.computeKind["global/"+key] = kind
		}
	}

	validateType(r, schemaSet["network"], networkFiles, func(s types.NetworkSpec) ([]string, []string) {
		return checkNetwork(s, ctx)
	})
	validateType(r, schemaSet["compute"], computeFiles, func(s types.ComputeSpec) ([]string, []string) {
		return checkCompute(s, ctx)
	})
	validateType(r, schemaSet["database"], databaseFiles, func(s types.DatabaseSpec) ([]string, []string) {
		return checkDatabase(s, ctx)
	})
	validateType(r, schemaSet["service"], serviceFiles, func(s types.ServiceSpec) ([]string, []string) {
		return checkService(s, ctx)
	})
	return nil
}

// validateType runs schema + semantic validation over every file of one type.
func validateType[T any](r *reporter, schema *jsonschema.Schema, files []fileOf[T], semantic func(T) (errs, warns []string)) {
	for _, f := range files {
		var errs, warns []string
		if f.parseErr != nil {
			r.report(f.path, []string{"parse error: " + f.parseErr.Error()}, nil)
			continue
		}
		if msgs := schemaErrors(schema, f.raw); len(msgs) > 0 {
			errs = append(errs, msgs...)
		} else {
			// Only run semantic checks once the document is structurally valid.
			e, w := semantic(f.spec)
			errs = append(errs, e...)
			warns = append(warns, w...)
		}
		r.report(f.path, errs, warns)
	}
}

// compileSchemas compiles every embedded JSON Schema, keyed by resource type.
func compileSchemas() (map[string]*jsonschema.Schema, error) {
	names := []string{"network", "compute", "database", "service"}
	c := jsonschema.NewCompiler()
	for _, n := range names {
		b, err := schemas.FS.ReadFile(n + ".json")
		if err != nil {
			return nil, err
		}
		if err := c.AddResource(n+".json", bytes.NewReader(b)); err != nil {
			return nil, err
		}
	}
	out := make(map[string]*jsonschema.Schema, len(names))
	for _, n := range names {
		sch, err := c.Compile(n + ".json")
		if err != nil {
			return nil, err
		}
		out[n] = sch
	}
	return out, nil
}

// schemaErrors validates a raw document against a schema, returning a flat list
// of human-readable messages (empty if valid).
func schemaErrors(schema *jsonschema.Schema, raw any) []string {
	doc, err := toJSONDoc(raw)
	if err != nil {
		return []string{"normalize document: " + err.Error()}
	}
	if err := schema.Validate(doc); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return flattenValidationError(ve)
		}
		return []string{err.Error()}
	}
	return nil
}

// toJSONDoc normalises a YAML-decoded value into canonical JSON types so the
// schema validator sees numbers as float64, etc.
func toJSONDoc(raw any) (any, error) {
	jb, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(jb, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// flattenValidationError turns a nested jsonschema error into leaf messages.
func flattenValidationError(ve *jsonschema.ValidationError) []string {
	if len(ve.Causes) == 0 {
		loc := ve.InstanceLocation
		if loc == "" {
			loc = "."
		}
		return []string{fmt.Sprintf("%s: %s", loc, ve.Message)}
	}
	var out []string
	for _, c := range ve.Causes {
		out = append(out, flattenValidationError(c)...)
	}
	sort.Strings(out)
	return out
}

// availableProviders returns the set of provider names defined in a region's
// provider config.
func availableProviders(providers map[string]map[string]any) map[string]bool {
	out := map[string]bool{}
	for name := range providers {
		out[name] = true
	}
	return out
}

// checkVariables validates variables.yaml, now slimmed to base_domain + ssh.
// Region selection and provider config moved to regions.yaml (see
// checkRegionsFile).
func checkVariables(r *reporter, vars types.EnvironmentVariables, path string) {
	var errs []string
	if strings.TrimSpace(vars.BaseDomain) == "" {
		errs = append(errs, "base_domain: required")
	}
	r.report(path, errs, nil)
}

// checkRegionsFile validates regions.yaml: at least one region, each with a slug
// and a non-empty providers block, and (when present) a global block carrying
// providers. Per-resource provider availability is checked against each region's
// own providers set in checkProviderAvailability.
func checkRegionsFile(r *reporter, table regions.Table, global *regions.Global, path string) {
	var errs []string
	if len(table) == 0 {
		errs = append(errs, "regions: at least one region must be defined")
	}
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ar := table[name]
		if strings.TrimSpace(ar.Slug) == "" {
			errs = append(errs, fmt.Sprintf("regions.%s: slug required", name))
		}
		if len(ar.Providers) == 0 {
			errs = append(errs, fmt.Sprintf("regions.%s: providers block required", name))
		}
		// The DNS authority is optional, but when declared both fields are
		// load-bearing: an empty provider/zone silently creates no (or zone-less)
		// records at apply. Caught here rather than failing at the DNS provider.
		if d := ar.Dns; d != nil {
			if strings.TrimSpace(d.Provider) == "" {
				errs = append(errs, fmt.Sprintf("regions.%s.dns: provider required when a dns authority is declared", name))
			}
			if strings.TrimSpace(d.Zone) == "" {
				errs = append(errs, fmt.Sprintf("regions.%s.dns: zone required when a dns authority is declared", name))
			}
		}
	}
	if global != nil {
		if len(global.Providers) == 0 {
			errs = append(errs, "global: providers block required when global is defined")
		}
		if strings.TrimSpace(global.PlacementRegion) == "" {
			errs = append(errs, "global.placementRegion: required when a global block is present")
		} else if _, err := table.Slug(global.PlacementRegion); err != nil {
			errs = append(errs, fmt.Sprintf("global.placementRegion: %q is not a defined region", global.PlacementRegion))
		}
	}
	r.report(path, errs, nil)
}

// providerErr reports a provider that is not available. A nil available set means
// provider availability is being checked separately (per region, against the
// shared resource set — see checkProviderAvailability), so the per-spec FK pass
// skips it.
// providerRef is one resource's declared provider together with its file path,
// for the per-region availability pass.
type providerRef struct {
	path     string
	provider string
}

// collectProviderRefs reads every resource file under base and returns each
// spec's path + declared provider. It only surfaces refs for files that parsed;
// malformed files are reported by the schema/FK pass in validateResourceSet.
func collectProviderRefs(base string, defaults types.ProviderDefaults) ([]providerRef, error) {
	var refs []providerRef
	if rs, err := refsOf[types.NetworkSpec](filepath.Join(base, "network"), func(s types.NetworkSpec) string {
		return types.ResolveProvider(s.Provider, "network", "", defaults)
	}); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	if rs, err := refsOf[types.ComputeSpec](filepath.Join(base, "compute"), func(s types.ComputeSpec) string {
		return types.ResolveProvider(s.Provider, "compute", "", defaults)
	}); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	if rs, err := refsOf[types.DatabaseSpec](filepath.Join(base, "database"), func(s types.DatabaseSpec) string {
		return types.ResolveProvider(s.Provider, "database", s.Engine, defaults)
	}); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	return refs, nil
}

// refsOf reads the resource files of one type under dir and returns each parsed
// spec's path + provider (extracted via providerOf). Parse failures are skipped
// here — validateResourceSet reports them.
func refsOf[T any](dir string, providerOf func(T) string) ([]providerRef, error) {
	files, err := readFiles[T](dir)
	if err != nil {
		return nil, err
	}
	var refs []providerRef
	for _, f := range files {
		if f.parseErr != nil {
			continue
		}
		p := providerOf(f.spec)
		if p == "" {
			continue // empty providers caught by per-spec check; skip availability check
		}
		refs = append(refs, providerRef{path: f.path, provider: p})
	}
	return refs, nil
}

// secretsProviderNames are the providers that can serve a service's runtime
// secrets. Keep in sync with registry.SecretsProviderName and the cases in
// registry.ServiceSecretsProvisioner.
var secretsProviderNames = []string{"infisical"}

// hasSecretsProvider reports whether an available provider set includes any
// secrets-capable provider.
func hasSecretsProvider(available map[string]bool) bool {
	for _, name := range secretsProviderNames {
		if available[name] {
			return true
		}
	}
	return false
}

// servicesWithSecrets returns the path of every parsed service file under base
// that declares at least one secret. A service with any secrets needs a secrets
// provider to write them to at deploy (program.provisionServiceSecrets fails
// otherwise), regardless of the secrets' source kind.
func servicesWithSecrets(base string) ([]string, error) {
	files, err := readFiles[types.ServiceSpec](filepath.Join(base, "service"))
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, f := range files {
		if f.parseErr != nil {
			continue
		}
		if len(f.spec.Secrets) > 0 {
			paths = append(paths, f.path)
		}
	}
	return paths, nil
}

// checkProviderAvailability verifies, for every region in the table, that each
// resource's declared provider is present in that region's providers block. The
// shared set deploys into every region, so a provider missing from any region is
// a failure for that region. Failures are reported under a per-region label
// rather than the resource file's path: the file's own OK/FAIL line is owned by
// the region-independent once-pass (validateResourceSet), and keying these on the
// same path would print a contradictory OK and FAIL for one file. Regions and the
// files within each are reported in sorted order for deterministic output.
func checkProviderAvailability(r *reporter, base string, table regions.Table, defaults types.ProviderDefaults) error {
	refs, err := collectProviderRefs(base, defaults)
	if err != nil {
		return err
	}
	secretSvcPaths, err := servicesWithSecrets(base)
	if err != nil {
		return err
	}
	regionNames := make([]string, 0, len(table))
	for region := range table {
		regionNames = append(regionNames, region)
	}
	sort.Strings(regionNames)

	for _, region := range regionNames {
		available := availableProviders(table[region].Providers)
		var msgs []string
		for _, ref := range refs {
			if !available[ref.provider] {
				msgs = append(msgs, fmt.Sprintf("%s: provider %q not defined in this region's regions.yaml providers block", ref.path, ref.provider))
			}
		}
		if len(secretSvcPaths) > 0 && !hasSecretsProvider(available) {
			for _, path := range secretSvcPaths {
				msgs = append(msgs, fmt.Sprintf("%s: declares secrets but this region's regions.yaml providers block has no secrets provider (one of: %s)", path, strings.Join(secretsProviderNames, ", ")))
			}
		}
		if len(msgs) > 0 {
			sort.Strings(msgs)
			r.fail(fmt.Sprintf("regions.yaml [%s] provider availability", region), msgs...)
		}
	}
	return nil
}

// checkGlobalProviderAvailability verifies each global resource's declared
// provider is present in the regions.yaml global providers block. The global
// slice is region-less, so it is checked once against the single global block
// (mirroring the per-region check in checkProviderAvailability).
func checkGlobalProviderAvailability(r *reporter, globalBase string, global *regions.Global, defaults types.ProviderDefaults) error {
	refs, err := collectProviderRefs(globalBase, defaults)
	if err != nil {
		return err
	}
	secretSvcPaths, err := servicesWithSecrets(globalBase)
	if err != nil {
		return err
	}
	available := availableProviders(global.Providers)
	var msgs []string
	for _, ref := range refs {
		if !available[ref.provider] {
			msgs = append(msgs, fmt.Sprintf("%s: provider %q not defined in regions.yaml global providers block", ref.path, ref.provider))
		}
	}
	if len(secretSvcPaths) > 0 && !hasSecretsProvider(available) {
		for _, path := range secretSvcPaths {
			msgs = append(msgs, fmt.Sprintf("%s: declares secrets but regions.yaml global providers block has no secrets provider (one of: %s)", path, strings.Join(secretsProviderNames, ", ")))
		}
	}
	if len(msgs) > 0 {
		sort.Strings(msgs)
		r.fail("regions.yaml [global] provider availability", msgs...)
	}
	return nil
}

// resolvedProviderErr returns an error when the effective provider (after applying
// defaults) is empty, or when the provider is not in the available set. It is used
// by the per-spec semantic checks; the per-region availability pass
// (checkProviderAvailability) is a separate step.
func resolvedProviderErr(specProvider, class, engine string, ctx regionContext) []string {
	effective := types.ResolveProvider(specProvider, class, engine, ctx.providerDefaults)
	if effective == "" {
		switch class {
		case "network", "compute":
			return []string{"provider: required — set provider: on this spec or add a providers.compute default in inforge.yaml"}
		case "database":
			return []string{fmt.Sprintf("provider: required — set provider: on this spec or add a providers.database.%s default in inforge.yaml", engine)}
		default:
			return []string{"provider: required"}
		}
	}
	if ctx.available != nil && !ctx.available[effective] {
		return []string{fmt.Sprintf("provider: %q not defined in this region's regions.yaml providers block", effective)}
	}
	return nil
}

func checkNetwork(s types.NetworkSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, resolvedProviderErr(s.Provider, "network", "", ctx)...)

	cidr, err := parseCIDR("cidr", s.CIDR)
	if err != nil {
		errs = append(errs, err.Error())
	}
	for i, sub := range s.Subnets {
		subnet, serr := parseCIDR(fmt.Sprintf("subnets[%d].cidr", i), sub.CIDR)
		if serr != nil {
			errs = append(errs, serr.Error())
		} else if cidr != nil && !cidrContains(cidr, subnet) {
			errs = append(errs, fmt.Sprintf("subnets[%d].cidr: %q is not within cidr %q", i, sub.CIDR, s.CIDR))
		}
	}
	return errs, warns
}

func checkCompute(s types.ComputeSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, resolvedProviderErr(s.Provider, "compute", "", ctx)...)

	if s.Kind == "cluster" {
		warns = append(warns, "kind: \"cluster\" is reserved and not implemented this phase")
	}
	// A compute attaching to a global network (network: global/<name>) is
	// recognized but rejected: materializing cross-region networking is not
	// supported yet. The global/ prefix is detected before the normal
	// network-existence check so the message is specific rather than "not found".
	if strings.HasPrefix(s.Network, "global/") {
		errs = append(errs, fmt.Sprintf("network: %q references a global network — cross-region networking is recognized but not supported yet", s.Network))
	} else if _, ok := ctx.networks[s.Network]; !ok {
		errs = append(errs, fmt.Sprintf("network: %q not found", s.Network))
	}
	if err := ctx.sizeTable.Resolve(s.Size); err != nil {
		errs = append(errs, err.Error())
	}
	if s.CloudInit != "" {
		if _, err := os.Stat(s.CloudInit); err != nil {
			errs = append(errs, fmt.Sprintf("cloud_init: file not found: %s", s.CloudInit))
		}
	}
	return errs, warns
}

func checkDatabase(s types.DatabaseSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, resolvedProviderErr(s.Provider, "database", s.Engine, ctx)...)
	return errs, warns
}

func checkService(s types.ServiceSpec, ctx regionContext) (errs, warns []string) {
	// A service on a global host (host: global/<name>) is rejected: a service that
	// runs on a global host is defined in the global slice itself, not referenced
	// from a region. Detected before host resolution so the message is specific.
	if strings.HasPrefix(s.Host, "global/") {
		errs = append(errs, fmt.Sprintf("host: %q references a global host — a service on a global host is defined in the global slice itself, not referenced from a region", s.Host))
		return errs, warns
	}
	_, ok := ctx.computeNames[s.Host]
	if !ok {
		if ctx.computeCanonical[s.Host] != "" {
			errs = append(errs, fmt.Sprintf("host: %q is an expanded specKey; use the bare compute name instead", s.Host))
		} else {
			errs = append(errs, fmt.Sprintf("host: %q does not resolve to a compute", s.Host))
		}
	} else {
		// s.Host is a bare compute name. The canonical map has a bare-name
		// entry only for single-instance computes; for multi-instance fall
		// back to instance 1 so the specKey-keyed maps are always reachable.
		canonical := ctx.computeCanonical[s.Host]
		if canonical == "" {
			canonical = naming.SpecKey(s.Host, 1)
		}
		kind := ctx.computeKind[canonical]
		if kind != "vm" {
			errs = append(errs, fmt.Sprintf("host: %q has kind %q; services require a vm host", s.Host, kind))
		}
		// A service's host DNS and its host's "<compute>.vm" record are derived
		// from the bare compute name (no instance index), so they cannot address
		// one instance of a multi-instance compute.
		if ctx.computeInstances[canonical] > 1 {
			errs = append(errs, fmt.Sprintf("host: %q is a multi-instance compute; a service host must be single-instance (the host DNS record cannot address one instance)", s.Host))
		}
		if !ctx.computeDeployer[canonical] {
			errs = append(errs, fmt.Sprintf("host: %q has no deploy_user; inforge provisions the service over SSH and requires one", s.Host))
		}
	}
	if s.Type == "container" {
		warns = append(warns, "type: \"container\" is reserved and not implemented this phase")
	}
	// Every service must declare the no-login user it runs as: the bootstrapper
	// drops privilege to this account before exec, so without it there is no
	// account to drop to. Required for secret-less and secret-bearing alike.
	if s.User == "" {
		errs = append(errs, "user: a service must declare the no-login user it runs as")
	}
	if len(s.Ingress) > 0 {
		host := ctx.computeCanonical[s.Host]
		if host == "" && ok {
			host = naming.SpecKey(s.Host, 1)
		}
		// nginx is always the host's sole public entry point when any ingress exists;
		// the service binds 127.0.0.1:target behind it. No host-level resource is
		// needed — realization is driven by ingress presence (provider from compute).
		for _, in := range s.Ingress {
			switch in.Type {
			case types.IngressTypeTLSTermination, types.IngressTypeForward:
			default:
				errs = append(errs, fmt.Sprintf("ingress: type %q is invalid; must be %q or %q", in.Type, types.IngressTypeTLSTermination, types.IngressTypeForward))
			}
			// Both ports are mandatory and explicit (no implicit defaults).
			if in.Listen < 1 || in.Listen > 65535 {
				errs = append(errs, fmt.Sprintf("ingress: listen %d is invalid; a public port (1..65535) is required on every ingress entry", in.Listen))
			}
			if in.Target < 1 || in.Target > 65535 {
				errs = append(errs, fmt.Sprintf("ingress: listen %d needs a target (1..65535) — the loopback port the service listens on", in.Listen))
			}
			// nginx binds *:listen (all interfaces, incl. loopback), so the service
			// cannot also bind 127.0.0.1:listen — listen and target must differ.
			if in.Listen == in.Target && in.Listen != 0 {
				errs = append(errs, fmt.Sprintf("ingress: listen and target must differ (both %d); nginx occupies the public port on all interfaces, so the service must listen on a different loopback port", in.Listen))
			}
			// The collision is host-wide: a target must not equal ANY public listen
			// port on the host (nginx holds *:<listen> on loopback too). The == own
			// listen case is reported above, so guard against a double error.
			if in.Listen != in.Target && len(ctx.portUsersByHost[host][in.Target]) > 0 {
				errs = append(errs, fmt.Sprintf("ingress: target %d collides with a public listen port on host %q; nginx occupies that port on all interfaces, so the service cannot bind it on loopback", in.Target, s.Host))
			}
			// A loopback target is bound by one process, so two different services
			// cannot share it (the same service may reuse its target across entries).
			if others := otherUsers(ctx.targetUsersByHost[host][in.Target], s.Name); len(others) > 0 {
				errs = append(errs, fmt.Sprintf("ingress: target %d on host %q is also used by service(s) %s; a loopback port belongs to a single service", in.Target, s.Host, strings.Join(others, ", ")))
			}
			if in.Type == types.IngressTypeForward {
				// vanity is an SNI/cert concept; a forward route has no SNI.
				if len(in.Vanity) > 0 {
					errs = append(errs, fmt.Sprintf("ingress: forward on listen %d has no SNI; remove vanity", in.Listen))
				}
				// A forward port is single-service-exclusive: nginx stream cannot
				// demux it, so no other ingress entry on the host may share it. (Since
				// forward is the only non-terminating type, this also guarantees any
				// shared listen port is tls-termination.)
				if users := ctx.portUsersByHost[host][in.Listen]; len(users) > 1 {
					others := otherUsers(users, s.Name)
					who := "another ingress entry on the same service"
					if len(others) > 0 {
						who = "service(s) " + strings.Join(others, ", ")
					}
					errs = append(errs, fmt.Sprintf("ingress: forward on listen %d is single-service-exclusive, but that port is also used by %s on host %q", in.Listen, who, s.Host))
				}
				// ACME owns :80 for HTTP-01 challenges, so a forward there collides
				// with any tls-termination on the same host.
				if in.Listen == 80 && ctx.tlsTermIngressByHost[host] {
					errs = append(errs, fmt.Sprintf("ingress: forward on :80 conflicts with a tls-termination on host %q (ACME owns :80 for HTTP-01 challenges)", s.Host))
				}
			}
		}
	}
	// Validate inline secrets: key namespace, source DSL, vault store presence.
	// Provider is not checked here — it is derived from the region config and
	// validated as part of provider availability checks, not per-secret.
	secKeys := make([]string, 0, len(s.Secrets))
	for k := range s.Secrets {
		secKeys = append(secKeys, k)
	}
	sort.Strings(secKeys)
	for _, k := range secKeys {
		if strings.HasPrefix(k, bootstrapper.ReservedEnvPrefix) {
			errs = append(errs, fmt.Sprintf("secrets.%s: env var name uses the reserved %s* namespace owned by inforge", k, bootstrapper.ReservedEnvPrefix))
		}
		parsed, err := ParseSource(s.Secrets[k])
		if err != nil {
			errs = append(errs, fmt.Sprintf("secrets.%s: %s", k, err.Error()))
			continue
		}
		if parsed.Kind == SourceVault {
			// A vault source's ciphertext must already exist in the committed store —
			// fail at validate time so the fix is cheap (run inforge secret set, commit).
			if ctx.encStore == nil {
				errs = append(errs, fmt.Sprintf("secrets.%s: source is vault but resources/<env>/%s does not exist — run `inforge secret init <env>`, then `inforge secret set <env> %s %s`", k, secretstore.FileName, s.Name, parsed.VaultKey))
			} else if _, ok := ctx.encStore.Get(s.Container, parsed.VaultKey); !ok {
				errs = append(errs, fmt.Sprintf("secrets.%s: no ciphertext for key %q in container %q in %s — run `inforge secret set <env> %s %s` and commit", k, parsed.VaultKey, s.Container, secretstore.FileName, s.Name, parsed.VaultKey))
			}
			continue
		}
		if parsed.Kind != SourceRef {
			continue
		}
		switch parsed.RefType {
		case "database":
			if parsed.RefOutput != "connectionUrl" {
				errs = append(errs, fmt.Sprintf("secrets.%s: unknown database output %q (want connectionUrl)", k, parsed.RefOutput))
			}
			if !ctx.databaseNames[parsed.RefName] {
				errs = append(errs, fmt.Sprintf("secrets.%s: database %q not found", k, parsed.RefName))
			}
		case "compute":
			if parsed.RefOutput != "publicIp" {
				errs = append(errs, fmt.Sprintf("secrets.%s: unknown compute output %q (want publicIp)", k, parsed.RefOutput))
			}
			if _, ok := ctx.computeKind[parsed.RefName]; !ok {
				errs = append(errs, fmt.Sprintf("secrets.%s: compute %q does not resolve to a compute instance", k, parsed.RefName))
			}
		}
	}
	return errs, warns
}

// otherUsers returns the port users that are not self, sorted and de-duplicated,
// for a stable, self-excluding conflict message.
func otherUsers(users []string, self string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range users {
		if u == self || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}
