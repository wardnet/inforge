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
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/regions"
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
	return len(g.Network)+len(g.Compute)+len(g.DNS)+len(g.Database)+
		len(g.Secrets)+len(g.Service)+len(g.TLSTermination) > 0
}

// regionContext holds the foreign-key targets and tables a region's semantic
// checks resolve against.
type regionContext struct {
	available        map[string]bool
	sizeTable        sizes.Table
	networks         map[string]types.NetworkSpec // specKey -> network
	computeKind      map[string]string            // expanded specKey -> kind
	computeCanonical map[string]string            // any accepted compute FK form -> canonical specKey
	computeDeployer  map[string]bool              // canonical specKey -> declares a deploy_user
	databaseNames    map[string]bool
	tlsByCompute     map[string]bool // canonical compute specKey -> has a tls-termination resource
	catchallByHost   map[string]int  // canonical compute specKey -> count of catch-all ingress services
}

// ValidateResources validates the single shared resource set under <dir>/<env>/
// and returns an error if any file failed. The resource set is defined once and
// instantiated into every region in regions.yaml, so its schema and foreign-key
// graph are region-independent and checked once; provider availability is then
// checked per region (a resource's provider must be declared in every region the
// set deploys into).
func ValidateResources(env, dir string) error {
	schemaSet, err := compileSchemas()
	if err != nil {
		return fmt.Errorf("compile schemas: %w", err)
	}

	regionTable, global, err := loader.LoadRegionTable(env, dir)
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
	globalBase := filepath.Join(base, "global")
	checkVariables(r, vars, filepath.Join(base, "variables.yaml"))
	checkRegionsFile(r, regionTable, global, filepath.Join(base, "regions.yaml"))

	// Validate the global slice in a GLOBAL-ONLY context (globalRefs nil): its FK
	// graph resolves only against global resources, so a global resource
	// referencing a regional one fails as not-found — enforcing "global → global
	// only". A global slice with resources but no global providers block is an error.
	if err := validateResourceSet(r, schemaSet, globalBase, nil, sizeTable, nil); err != nil {
		return err
	}
	if global == nil && globalHasResources(globalRes) {
		r.fail("regions.yaml [global]", "resources/"+env+"/global declares resources but regions.yaml has no global providers block")
	}

	// Validate the shared regional set once: schema + the region-independent FK
	// graph, with the global outputs injected so a regional secrets `ref:` may
	// resolve a global/<name> target. Provider availability is region-specific, so
	// it is skipped here (available nil) and checked separately per region below.
	if err := validateResourceSet(r, schemaSet, base, nil, sizeTable, buildGlobalRefs(globalRes)); err != nil {
		return err
	}

	// Per-region provider availability: the same set deploys into every region, so
	// each resource's provider must be declared in that region's providers block.
	if err := checkProviderAvailability(r, base, regionTable); err != nil {
		return err
	}
	// The global slice realizes against the regions.yaml global providers block.
	if global != nil {
		if err := checkGlobalProviderAvailability(r, globalBase, global); err != nil {
			return err
		}
	}

	if r.failed {
		return errors.New("validation failed")
	}
	return nil
}

func validateResourceSet(r *reporter, schemaSet map[string]*jsonschema.Schema, base string, available map[string]bool, sizeTable sizes.Table, global *globalRefs) error {
	networkFiles, err := readFiles[types.NetworkSpec](filepath.Join(base, "network"))
	if err != nil {
		return err
	}
	computeDir := filepath.Join(base, "compute")
	computeFiles, err := readFiles[types.ComputeSpec](computeDir)
	if err != nil {
		return err
	}
	dnsFiles, err := readFiles[types.DnsSpec](filepath.Join(base, "dns"))
	if err != nil {
		return err
	}
	databaseFiles, err := readFiles[types.DatabaseSpec](filepath.Join(base, "database"))
	if err != nil {
		return err
	}
	secretsFiles, err := readFiles[types.SecretsSpec](filepath.Join(base, "secrets"))
	if err != nil {
		return err
	}
	serviceFiles, err := readFiles[types.ServiceSpec](filepath.Join(base, "service"))
	if err != nil {
		return err
	}
	tlsFiles, err := readFiles[types.TLSTerminationSpec](filepath.Join(base, "tls-termination"))
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
		available:        available,
		sizeTable:        sizeTable,
		networks:         map[string]types.NetworkSpec{},
		computeKind:      map[string]string{},
		computeCanonical: map[string]string{},
		computeDeployer:  map[string]bool{},
		databaseNames:    map[string]bool{},
		tlsByCompute:     map[string]bool{},
		catchallByHost:   map[string]int{},
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
			ctx.computeDeployer[key] = hasDeployer
		}
		if f.spec.InstanceCount == 1 {
			// bridge and bridge-01 both reference the same host.
			ctx.computeKind[f.spec.Name] = f.spec.Kind
		}
	}
	// Canonicalization (any compute FK form -> expanded specKey) is shared with
	// the program so validation and realization agree on host identity.
	ctx.computeCanonical = naming.CanonicalComputeKeys(computeSpecs)
	for _, f := range databaseFiles {
		ctx.databaseNames[f.spec.Name] = true
	}
	for _, f := range tlsFiles {
		if c, ok := ctx.computeCanonical[f.spec.Compute]; ok {
			ctx.tlsByCompute[c] = true
		}
	}
	// Count catch-all ingress services per host so checkService can enforce the
	// at-most-one rule (the host's whole route table funnels its unmatched SNIs to
	// a single dispatcher).
	for _, f := range serviceFiles {
		if f.spec.Ingress == nil || !f.spec.Ingress.Catchall {
			continue
		}
		if c, ok := ctx.computeCanonical[f.spec.Host]; ok {
			ctx.catchallByHost[c]++
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
	validateType(r, schemaSet["dns"], dnsFiles, func(s types.DnsSpec) ([]string, []string) {
		return checkDNS(s, ctx)
	})
	validateType(r, schemaSet["database"], databaseFiles, func(s types.DatabaseSpec) ([]string, []string) {
		return checkDatabase(s, ctx)
	})
	validateType(r, schemaSet["secrets"], secretsFiles, func(s types.SecretsSpec) ([]string, []string) {
		return checkSecrets(s, ctx)
	})
	validateType(r, schemaSet["service"], serviceFiles, func(s types.ServiceSpec) ([]string, []string) {
		return checkService(s, ctx)
	})
	validateType(r, schemaSet["tls-termination"], tlsFiles, func(s types.TLSTerminationSpec) ([]string, []string) {
		return checkTLSTermination(s, ctx)
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
	names := []string{"network", "compute", "dns", "database", "secrets", "service", "tls-termination"}
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
	}
	if global != nil && len(global.Providers) == 0 {
		errs = append(errs, "global: providers block required when global is defined")
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
func collectProviderRefs(base string) ([]providerRef, error) {
	var refs []providerRef
	if rs, err := refsOf[types.NetworkSpec](filepath.Join(base, "network"), func(s types.NetworkSpec) string { return s.Provider }); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	if rs, err := refsOf[types.ComputeSpec](filepath.Join(base, "compute"), func(s types.ComputeSpec) string { return s.Provider }); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	if rs, err := refsOf[types.DnsSpec](filepath.Join(base, "dns"), func(s types.DnsSpec) string { return s.Provider }); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	if rs, err := refsOf[types.DatabaseSpec](filepath.Join(base, "database"), func(s types.DatabaseSpec) string { return s.Provider }); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	if rs, err := refsOf[types.SecretsSpec](filepath.Join(base, "secrets"), func(s types.SecretsSpec) string { return s.Provider }); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	if rs, err := refsOf[types.ServiceSpec](filepath.Join(base, "service"), func(s types.ServiceSpec) string { return s.Provider }); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	if rs, err := refsOf[types.TLSTerminationSpec](filepath.Join(base, "tls-termination"), func(s types.TLSTerminationSpec) string { return s.Provider }); err != nil {
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
		refs = append(refs, providerRef{path: f.path, provider: providerOf(f.spec)})
	}
	return refs, nil
}

// checkProviderAvailability verifies, for every region in the table, that each
// resource's declared provider is present in that region's providers block. The
// shared set deploys into every region, so a provider missing from any region is
// a failure for that region. Failures are reported under a per-region label
// rather than the resource file's path: the file's own OK/FAIL line is owned by
// the region-independent once-pass (validateResourceSet), and keying these on the
// same path would print a contradictory OK and FAIL for one file. Regions and the
// files within each are reported in sorted order for deterministic output.
func checkProviderAvailability(r *reporter, base string, table regions.Table) error {
	refs, err := collectProviderRefs(base)
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
func checkGlobalProviderAvailability(r *reporter, globalBase string, global *regions.Global) error {
	refs, err := collectProviderRefs(globalBase)
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
	if len(msgs) > 0 {
		sort.Strings(msgs)
		r.fail("regions.yaml [global] provider availability", msgs...)
	}
	return nil
}

func providerErr(provider string, available map[string]bool) []string {
	if available == nil {
		return nil
	}
	if !available[provider] {
		return []string{fmt.Sprintf("provider: %q not defined in this region's regions.yaml providers block", provider)}
	}
	return nil
}

func checkNetwork(s types.NetworkSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, providerErr(s.Provider, ctx.available)...)

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
	errs = append(errs, providerErr(s.Provider, ctx.available)...)

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

func checkDNS(s types.DnsSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, providerErr(s.Provider, ctx.available)...)
	if _, ok := ctx.computeKind[s.Compute]; !ok {
		errs = append(errs, fmt.Sprintf("compute: %q does not resolve to a compute instance", s.Compute))
	}
	return errs, warns
}

func checkDatabase(s types.DatabaseSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, providerErr(s.Provider, ctx.available)...)
	return errs, warns
}

func checkSecrets(s types.SecretsSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, providerErr(s.Provider, ctx.available)...)
	// Iterate entries in a stable order for deterministic output.
	keys := make([]string, 0, len(s.Secrets))
	for k := range s.Secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		src := s.Secrets[k].Source
		parsed, err := ParseSource(src)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", k, err.Error()))
			continue
		}
		if parsed.Kind != SourceRef {
			continue
		}
		switch parsed.RefType {
		case "database":
			if parsed.RefOutput != "connectionUrl" {
				errs = append(errs, fmt.Sprintf("%s: unknown database output %q (want connectionUrl)", k, parsed.RefOutput))
			}
			if !ctx.databaseNames[parsed.RefName] {
				errs = append(errs, fmt.Sprintf("%s: database %q not found", k, parsed.RefName))
			}
		case "compute":
			if parsed.RefOutput != "publicIp" {
				errs = append(errs, fmt.Sprintf("%s: unknown compute output %q (want publicIp)", k, parsed.RefOutput))
			}
			if _, ok := ctx.computeKind[parsed.RefName]; !ok {
				errs = append(errs, fmt.Sprintf("%s: compute %q does not resolve to a compute instance", k, parsed.RefName))
			}
		}
	}
	return errs, warns
}

func checkService(s types.ServiceSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, providerErr(s.Provider, ctx.available)...)
	// A service on a global host (host: global/<name>) is rejected: a service that
	// runs on a global host is defined in the global slice itself, not referenced
	// from a region. Detected before host resolution so the message is specific.
	if strings.HasPrefix(s.Host, "global/") {
		errs = append(errs, fmt.Sprintf("host: %q references a global host — a service on a global host is defined in the global slice itself, not referenced from a region", s.Host))
		return errs, warns
	}
	kind, ok := ctx.computeKind[s.Host]
	if !ok {
		errs = append(errs, fmt.Sprintf("host: %q does not resolve to a compute instance", s.Host))
	} else if kind != "vm" {
		errs = append(errs, fmt.Sprintf("host: %q has kind %q; services require a vm host", s.Host, kind))
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
	// inforge deploy provisions the service's unit + folder over SSH as the
	// host's deploy_user, so a service host must declare one — caught here
	// instead of failing at pulumi up.
	if ok && !ctx.computeDeployer[ctx.computeCanonical[s.Host]] {
		errs = append(errs, fmt.Sprintf("host: %q has no deploy_user; inforge provisions the service over SSH and requires one", s.Host))
	}
	if s.Ingress != nil {
		host := ctx.computeCanonical[s.Host]
		if ok && !ctx.tlsByCompute[host] {
			errs = append(errs, fmt.Sprintf("ingress: host %q has no tls-termination resource to terminate it", s.Host))
		}
		// A catch-all forwards every unmatched SNI, so it is inherently passthrough;
		// terminating arbitrary SNIs would need on-demand certs, which we don't do.
		if s.Ingress.Catchall && s.Ingress.TLS == types.IngressTLSTerminate {
			errs = append(errs, "ingress: catchall is passthrough-only; remove tls: terminate")
		}
		// At most one catch-all per host: its unmatched-SNI traffic funnels to a
		// single dispatcher. Caught here (and again at preview/deploy) rather than
		// silently picking one.
		if s.Ingress.Catchall && ok && ctx.catchallByHost[host] > 1 {
			errs = append(errs, fmt.Sprintf("ingress: host %q has %d catch-all services; at most one is allowed", s.Host, ctx.catchallByHost[host]))
		}
		// proxy_protocol only affects passthrough/catch-all upstreams; on a
		// terminate route it is silently ignored, so flag the likely mistake.
		if s.Ingress.ProxyProtocol != "" && s.Ingress.Mode() == types.IngressTLSTerminate {
			warns = append(warns, "ingress: proxy_protocol has no effect on a terminate route")
		}
	}
	return errs, warns
}

func checkTLSTermination(s types.TLSTerminationSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, providerErr(s.Provider, ctx.available)...)
	kind, ok := ctx.computeKind[s.Compute]
	if !ok {
		errs = append(errs, fmt.Sprintf("compute: %q does not resolve to a compute instance", s.Compute))
	} else if kind != "vm" {
		errs = append(errs, fmt.Sprintf("compute: %q has kind %q; tls-termination requires a vm host", s.Compute, kind))
	}
	// The terminator is realized over SSH as the host's deploy user. Without one
	// inforge can't connect, so a config that omits it would only fail at deploy
	// time — catch it here instead.
	if ok && !ctx.computeDeployer[ctx.computeCanonical[s.Compute]] {
		errs = append(errs, fmt.Sprintf("compute: %q has no deploy_user; tls-termination is realized over SSH and requires one", s.Compute))
	}
	return errs, warns
}
