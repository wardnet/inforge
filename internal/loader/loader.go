// Package loader reads a project's on-disk resource definitions for a single
// environment: variables.yaml, the optional per-env region/size tables, and
// the per-region resource files. It applies the defaults that yaml.v3 cannot,
// substitutes ${ENV_VAR} references in variables.yaml, and resolves cloud-init
// paths. Cross-resource and semantic validation lives in internal/validate.
package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/sizes"
	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
)

// envVarPattern matches ${NAME} references substituted in variables.yaml.
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// envDir returns the directory holding one environment's definitions.
func envDir(env, dir string) string {
	return filepath.Join(dir, env)
}

// substituteEnvVars walks a decoded YAML value and replaces ${NAME} in every
// string with the corresponding environment variable. When lenient is false,
// an unset or empty variable is an error; when true, the reference is replaced
// with an empty string so structural validation can proceed without credentials.
func substituteEnvVars(v any, lenient bool) (any, error) {
	switch t := v.(type) {
	case string:
		var subErr error
		out := envVarPattern.ReplaceAllStringFunc(t, func(m string) string {
			key := strings.TrimSuffix(strings.TrimPrefix(m, "${"), "}")
			val := os.Getenv(key)
			if val == "" && !lenient {
				subErr = fmt.Errorf("missing required env var: %s", key)
			}
			return val
		})
		if subErr != nil {
			return nil, subErr
		}
		return out, nil
	case []any:
		for i, e := range t {
			ne, err := substituteEnvVars(e, lenient)
			if err != nil {
				return nil, err
			}
			t[i] = ne
		}
		return t, nil
	case map[string]any:
		for k, e := range t {
			ne, err := substituteEnvVars(e, lenient)
			if err != nil {
				return nil, err
			}
			t[k] = ne
		}
		return t, nil
	default:
		return v, nil
	}
}

func loadVariables(env, dir string, lenient bool) (types.EnvironmentVariables, error) {
	var vars types.EnvironmentVariables
	path := filepath.Join(envDir(env, dir), "variables.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		return vars, fmt.Errorf("read variables: %w", err)
	}
	var raw any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return vars, fmt.Errorf("parse variables: %w", err)
	}
	subbed, err := substituteEnvVars(raw, lenient)
	if err != nil {
		return vars, fmt.Errorf("%s: %w", path, err)
	}
	rb, err := yaml.Marshal(subbed)
	if err != nil {
		return vars, fmt.Errorf("re-encode variables: %w", err)
	}
	if err := yaml.Unmarshal(rb, &vars); err != nil {
		return vars, fmt.Errorf("decode variables: %w", err)
	}
	return vars, nil
}

// LoadVariables reads and parses <dir>/<env>/variables.yaml, substituting
// ${ENV_VAR} references. Missing variables are an error; use LoadVariablesLenient
// for contexts where credentials are not available (e.g. schema validation).
func LoadVariables(env, dir string) (types.EnvironmentVariables, error) {
	return loadVariables(env, dir, false)
}

// LoadVariablesLenient is like LoadVariables but silently replaces missing env
// vars with an empty string rather than returning an error. Use this for
// structural validation that doesn't require actual credential values.
func LoadVariablesLenient(env, dir string) (types.EnvironmentVariables, error) {
	return loadVariables(env, dir, true)
}

// LoadRegionTable returns the region table for an environment: the per-env
// resources/<env>/regions.yaml if present (replacing the defaults wholesale),
// otherwise the built-in defaults.
func LoadRegionTable(env, dir string) (regions.Table, error) {
	path := filepath.Join(envDir(env, dir), "regions.yaml")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return regions.DefaultTable(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read regions table: %w", err)
	}
	var tbl regions.Table
	if err := yaml.Unmarshal(b, &tbl); err != nil {
		return nil, fmt.Errorf("parse regions table: %w", err)
	}
	return tbl, nil
}

// LoadSizeTable returns the size table for an environment: the per-env
// resources/<env>/sizes.yaml if present (replacing the defaults wholesale),
// otherwise the built-in defaults. The file is a YAML list of size names, e.g.
// `[SMALL, MEDIUM, LARGE]` — the size table is cloud-agnostic vocabulary with no
// cpus/memory payload (a provider maps a size name to a concrete SKU).
func LoadSizeTable(env, dir string) (sizes.Table, error) {
	path := filepath.Join(envDir(env, dir), "sizes.yaml")
	b, err := os.ReadFile(path)
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

// loadType reads every .yaml/.yml file in dir into a T. A missing directory is
// not an error (it yields no resources).
func loadType[T any](dir string) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []T
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var v T
		if err := yaml.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		out = append(out, v)
	}
	return out, nil
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// RegionDirs returns the names of the region subdirectories under an
// environment (every directory entry below <dir>/<env>/).
func RegionDirs(env, dir string) ([]string, error) {
	entries, err := os.ReadDir(envDir(env, dir))
	if err != nil {
		return nil, fmt.Errorf("read env dir: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// LoadResources walks each region subdirectory under <dir>/<env>/ and returns
// the parsed, default-normalised resources keyed by region directory name.
// cloud_init paths are resolved to absolute paths relative to the compute dir.
func LoadResources(env, dir string) (map[string]types.Resources, error) {
	regionDirs, err := RegionDirs(env, dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]types.Resources, len(regionDirs))
	for _, region := range regionDirs {
		base := filepath.Join(envDir(env, dir), region)
		var res types.Resources

		if res.Network, err = loadType[types.NetworkSpec](filepath.Join(base, "network")); err != nil {
			return nil, err
		}
		computeDir := filepath.Join(base, "compute")
		if res.Compute, err = loadType[types.ComputeSpec](computeDir); err != nil {
			return nil, err
		}
		if res.DNS, err = loadType[types.DnsSpec](filepath.Join(base, "dns")); err != nil {
			return nil, err
		}
		if res.Database, err = loadType[types.DatabaseSpec](filepath.Join(base, "database")); err != nil {
			return nil, err
		}
		if res.Secrets, err = loadType[types.SecretsSpec](filepath.Join(base, "secrets")); err != nil {
			return nil, err
		}
		if res.Service, err = loadType[types.ServiceSpec](filepath.Join(base, "service")); err != nil {
			return nil, err
		}

		applyDefaults(&res, computeDir)
		out[region] = res
	}
	return out, nil
}

// applyDefaults fills in the defaults yaml.v3 cannot and resolves cloud_init
// paths relative to the compute directory. The per-spec normalisers are
// exported so internal/validate applies identical defaults.
func applyDefaults(res *types.Resources, computeDir string) {
	for i := range res.Network {
		NormalizeNetwork(&res.Network[i])
	}
	for i := range res.Compute {
		NormalizeCompute(&res.Compute[i], computeDir)
	}
	for i := range res.Database {
		NormalizeDatabase(&res.Database[i])
	}
	for i := range res.Service {
		NormalizeService(&res.Service[i])
	}
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

// NormalizeDatabase applies database defaults (branch defaults to "main").
func NormalizeDatabase(d *types.DatabaseSpec) {
	if d.Branch == "" {
		d.Branch = "main"
	}
}

// NormalizeService applies service defaults (type defaults to "raw").
func NormalizeService(s *types.ServiceSpec) {
	if s.Type == "" {
		s.Type = "raw"
	}
}
