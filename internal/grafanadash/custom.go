package grafanadash

import (
	"encoding/json"
	"fmt"

	"github.com/wardnet/inforge/internal/yamldoc"
)

// Custom normalizes a Grafana-exported dashboard (ADR-0038) into the JSON body the
// pulumiverse/grafana provider's ConfigJson expects. A custom dashboard is authored in
// the Grafana UI, exported (JSON model, or YAML), and committed under
// observability/dashboards/; inforge does not invent a DSL for it. Normalization is
// minimal and deterministic:
//
//   - YAML input is decoded to the same map model JSON would produce.
//   - An API-shaped export ({"dashboard": {...}, "meta": {...}}) is unwrapped to its
//     inner dashboard model, so both the "copy JSON model" and "get by UID" forms work.
//   - The dashboard `uid` is overwritten with the env-prefixed uid so the same file
//     deployed to two envs sharing one Grafana org never collides; the author's `title`,
//     panels, and queries are otherwise left untouched.
//
// name is the file's slug (used only in error messages); uid is grafana.DashboardUID.
func Custom(name, uid string, raw []byte, isYAML bool) (string, error) {
	var model map[string]any
	if isYAML {
		// Through the one reader, decoded LITERALLY — and that is the whole point. A
		// dashboard is full of Grafana's OWN ${DS_FOO} template syntax; no resolver
		// claims it, nothing asks to resolve it, and it survives verbatim. Reading is
		// not resolving.
		doc, err := yamldoc.Parse(name, raw)
		if err != nil {
			return "", fmt.Errorf("grafanadash: custom dashboard %q: %w", name, err)
		}
		if err := doc.Decode(&model); err != nil {
			return "", fmt.Errorf("grafanadash: custom dashboard %q: %w", name, err)
		}
	} else {
		if err := json.Unmarshal(raw, &model); err != nil {
			return "", fmt.Errorf("grafanadash: custom dashboard %q: parse JSON: %w", name, err)
		}
	}
	if model == nil {
		return "", fmt.Errorf("grafanadash: custom dashboard %q: empty document", name)
	}

	// Unwrap an API-shaped export: {"dashboard": {...}, "meta": {...}}.
	if inner, ok := model["dashboard"].(map[string]any); ok {
		model = inner
	}

	// Env-prefix the uid so a shared Grafana org keeps per-env copies distinct; the
	// folder placement (caller) plus this uid make the dashboard unambiguously this env's.
	model["uid"] = uid

	b, err := json.Marshal(model)
	if err != nil {
		return "", fmt.Errorf("grafanadash: custom dashboard %q: marshal: %w", name, err)
	}
	return string(b), nil
}
