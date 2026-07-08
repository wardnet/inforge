package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// CustomDashboard is one Grafana-exported dashboard file discovered under an env's
// observability/dashboards/ directory (ADR-0038 slice 2). Name is a stable slug derived
// from the filename (used to build the env-prefixed dashboard UID and the Pulumi resource
// name); IsYAML selects the parser; Data is the raw file bytes, normalized by
// grafanadash.Custom before it reaches the provider.
type CustomDashboard struct {
	Name   string
	IsYAML bool
	Data   []byte
}

// slugUnsafe matches every run of characters that are not lowercase-alphanumeric.
var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// dashboardSlug turns a dashboard filename base into a stable, uid-safe slug
// (lowercase, non-alphanumerics collapsed to single dashes, trimmed).
func dashboardSlug(base string) string {
	s := slugUnsafe.ReplaceAllString(strings.ToLower(base), "-")
	return strings.Trim(s, "-")
}

// LoadCustomDashboards reads every *.json / *.yaml / *.yml file under
// <dir>/<env>/observability/dashboards/ and returns them sorted by slug for a
// deterministic deploy. A missing directory is not an error — custom dashboards are
// optional. Two files whose names collapse to the same slug are rejected (their
// env-prefixed UIDs would collide).
func LoadCustomDashboards(env, dir string) ([]CustomDashboard, error) {
	root := filepath.Join(envDir(env, dir), "observability", "dashboards")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("loader: read observability/dashboards: %w", err)
	}

	seen := make(map[string]string, len(entries))
	out := make([]CustomDashboard, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		isYAML := ext == ".yaml" || ext == ".yml"
		if ext != ".json" && !isYAML {
			continue
		}
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		slug := dashboardSlug(base)
		if slug == "" {
			return nil, fmt.Errorf("loader: custom dashboard %q has no usable name", e.Name())
		}
		if prev, dup := seen[slug]; dup {
			return nil, fmt.Errorf("loader: custom dashboards %q and %q both resolve to slug %q", prev, e.Name(), slug)
		}
		seen[slug] = e.Name()

		data, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("loader: read custom dashboard %q: %w", e.Name(), err)
		}
		out = append(out, CustomDashboard{Name: slug, IsYAML: isYAML, Data: data})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
