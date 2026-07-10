package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/wardnet/inforge/internal/types"
	"gopkg.in/yaml.v3"
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

		data, err := os.ReadFile(filepath.Join(root, e.Name())) // #nosec G304 -- root derives from operator-supplied --dir/env; e.Name() is an entry from that same directory
		if err != nil {
			return nil, fmt.Errorf("loader: read custom dashboard %q: %w", e.Name(), err)
		}
		out = append(out, CustomDashboard{Name: slug, IsYAML: isYAML, Data: data})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LoadNotifications reads <dir>/<env>/observability/notifications.yaml — the env's
// contact points + notification profiles (ADR-0038 slice 3). A missing file is not
// an error: an env may manage dashboards/alerts without custom routing.
func LoadNotifications(env, dir string) (types.NotificationsSpec, error) {
	var spec types.NotificationsSpec
	path := filepath.Join(envDir(env, dir), "observability", "notifications.yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- path derives from operator-supplied --dir/env
	if err != nil {
		if os.IsNotExist(err) {
			return spec, nil
		}
		return spec, fmt.Errorf("loader: read notifications.yaml: %w", err)
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return spec, fmt.Errorf("loader: parse notifications.yaml: %w", err)
	}
	return spec, nil
}

// LoadAlerts reads <dir>/<env>/observability/alerts.yaml — the env's custom alert
// specs (ADR-0038 slice 3), alongside the generated built-ins. A missing file is
// not an error. Free-text fields are trimmed.
func LoadAlerts(env, dir string) (types.AlertsSpec, error) {
	var spec types.AlertsSpec
	path := filepath.Join(envDir(env, dir), "observability", "alerts.yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- path derives from operator-supplied --dir/env
	if err != nil {
		if os.IsNotExist(err) {
			return spec, nil
		}
		return spec, fmt.Errorf("loader: read alerts.yaml: %w", err)
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return spec, fmt.Errorf("loader: parse alerts.yaml: %w", err)
	}
	for i := range spec.Alerts {
		a := &spec.Alerts[i]
		a.Name = strings.TrimSpace(a.Name)
		a.Expr = strings.TrimSpace(a.Expr)
		a.Condition = strings.TrimSpace(a.Condition)
		a.For = strings.TrimSpace(a.For)
		a.Severity = strings.TrimSpace(a.Severity)
		a.Profile = strings.TrimSpace(a.Profile)
		a.Summary = strings.TrimSpace(a.Summary)
	}
	return spec, nil
}
