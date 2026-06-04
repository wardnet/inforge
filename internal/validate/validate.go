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

// regionContext holds the foreign-key targets and tables a region's semantic
// checks resolve against.
type regionContext struct {
	available     map[string]bool
	sizeTable     sizes.Table
	networks      map[string]types.NetworkSpec // specKey -> network
	computeKind   map[string]string            // expanded specKey -> kind
	databaseNames map[string]bool
}

// ValidateResources validates every region under <dir>/<env>/ and returns an
// error if any file failed.
func ValidateResources(env, dir string) error {
	schemaSet, err := compileSchemas()
	if err != nil {
		return fmt.Errorf("compile schemas: %w", err)
	}

	regionTable, err := loader.LoadRegionTable(env, dir)
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

	r := &reporter{}
	checkVariables(r, vars, regionTable, filepath.Join(dir, env, "variables.yaml"))

	declared := map[string]bool{}
	for _, re := range vars.Regions {
		declared[re.Name] = true
	}

	regionDirs, err := loader.RegionDirs(env, dir)
	if err != nil {
		return err
	}

	for _, region := range regionDirs {
		regionBase := filepath.Join(dir, env, region)
		if !declared[region] {
			r.fail(regionBase, fmt.Sprintf("region %q is not declared in variables.yaml regions[]", region))
		} else if err := regionTable.Validate(region); err != nil {
			r.fail(regionBase, err.Error())
		}

		if err := validateRegion(r, schemaSet, regionBase, region, vars, sizeTable); err != nil {
			return err
		}
	}

	if r.failed {
		return errors.New("validation failed")
	}
	return nil
}

func validateRegion(r *reporter, schemaSet map[string]*jsonschema.Schema, regionBase, region string, vars types.EnvironmentVariables, sizeTable sizes.Table) error {
	networkFiles, err := readFiles[types.NetworkSpec](filepath.Join(regionBase, "network"))
	if err != nil {
		return err
	}
	computeDir := filepath.Join(regionBase, "compute")
	computeFiles, err := readFiles[types.ComputeSpec](computeDir)
	if err != nil {
		return err
	}
	dnsFiles, err := readFiles[types.DnsSpec](filepath.Join(regionBase, "dns"))
	if err != nil {
		return err
	}
	databaseFiles, err := readFiles[types.DatabaseSpec](filepath.Join(regionBase, "database"))
	if err != nil {
		return err
	}
	secretsFiles, err := readFiles[types.SecretsSpec](filepath.Join(regionBase, "secrets"))
	if err != nil {
		return err
	}
	serviceFiles, err := readFiles[types.ServiceSpec](filepath.Join(regionBase, "service"))
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
		available:     availableProviders(vars, region),
		sizeTable:     sizeTable,
		networks:      map[string]types.NetworkSpec{},
		computeKind:   map[string]string{},
		databaseNames: map[string]bool{},
	}
	for _, f := range networkFiles {
		ctx.networks[f.spec.Name] = f.spec
	}
	for _, f := range computeFiles {
		for i := 1; i <= f.spec.InstanceCount; i++ {
			ctx.computeKind[naming.SpecKey(f.spec.Name, i)] = f.spec.Kind
		}
	}
	for _, f := range databaseFiles {
		ctx.databaseNames[f.spec.Name] = true
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
	names := []string{"network", "compute", "dns", "database", "secrets", "service"}
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

// availableProviders returns the set of provider names available to a region:
// the global providers plus any declared in the region's overrides.
func availableProviders(vars types.EnvironmentVariables, region string) map[string]bool {
	out := map[string]bool{}
	for name := range vars.Providers {
		out[name] = true
	}
	for _, re := range vars.Regions {
		if re.Name == region {
			for name := range re.Providers {
				out[name] = true
			}
		}
	}
	return out
}

func checkVariables(r *reporter, vars types.EnvironmentVariables, table regions.Table, path string) {
	var errs []string
	if strings.TrimSpace(vars.BaseDomain) == "" {
		errs = append(errs, "base_domain: required")
	}
	for _, re := range vars.Regions {
		if err := table.Validate(re.Name); err != nil {
			errs = append(errs, err.Error())
		}
	}
	r.report(path, errs, nil)
}

func providerErr(provider string, available map[string]bool) []string {
	if !available[provider] {
		return []string{fmt.Sprintf("provider: %q not defined in variables.yaml providers", provider)}
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
	if _, ok := ctx.networks[s.Network]; !ok {
		errs = append(errs, fmt.Sprintf("network: %q not found", s.Network))
	}
	if _, err := ctx.sizeTable.Resolve(s.Size); err != nil {
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
	kind, ok := ctx.computeKind[s.Host]
	if !ok {
		errs = append(errs, fmt.Sprintf("host: %q does not resolve to a compute instance", s.Host))
	} else if kind != "vm" {
		errs = append(errs, fmt.Sprintf("host: %q has kind %q; services require a vm host", s.Host, kind))
	}
	if s.Type == "container" {
		warns = append(warns, "type: \"container\" is reserved and not implemented this phase")
	}
	return errs, warns
}
