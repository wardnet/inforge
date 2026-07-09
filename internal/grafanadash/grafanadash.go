// Package grafanadash renders inforge's built-in Grafana dashboards as JSON, from
// the metrics inforge owns (ADR-0038): host metrics (ADR-0031), Postgres metrics
// (ADR-0037), and — added in a later slice — wardnet-cloud's RED + domain metrics.
// It is pure and dependency-free (like internal/otelcol and internal/nginx): it builds
// the dashboard model as nested maps and marshals it, so the Pulumi deploy path just
// feeds the JSON to grafana.oss.Dashboard.ConfigJson. Every dashboard is scoped to one
// identity env: its queries filter on deployment_environment_name so a shared Grafana
// org shows only this env's data, and its UID/title are env-prefixed by the caller.
package grafanadash

import (
	"encoding/json"
	"fmt"
)

// PromDatasourceUID is the Prometheus datasource the panels query. "grafanacloud-prom"
// is the fixed UID Grafana Cloud assigns the hosted Prometheus; a later slice can make
// it configurable for self-managed Grafana.
const PromDatasourceUID = "grafanacloud-prom"

// schemaVersion is the classic dashboard schema we author. Grafana accepts it and
// migrates it internally — the same path we validated by hand (ADR-0038).
const schemaVersion = 39

func ds() map[string]any {
	return map[string]any{"type": "prometheus", "uid": PromDatasourceUID}
}

func gp(x, y, w, h int) map[string]any {
	return map[string]any{"x": x, "y": y, "w": w, "h": h}
}

func target(expr, legend string) map[string]any {
	return map[string]any{"refId": "A", "expr": expr, "datasource": ds(), "legendFormat": legend, "range": true}
}

// targets builds a target list from (expr, legend) pairs, assigning refIds A, B, C…
func targets(pairs ...[2]string) []map[string]any {
	out := make([]map[string]any, 0, len(pairs))
	for i, p := range pairs {
		out = append(out, map[string]any{
			"refId": string(rune('A' + i)), "expr": p[0], "datasource": ds(),
			"legendFormat": p[1], "range": true,
		})
	}
	return out
}

func thresholds(steps ...[2]any) map[string]any {
	out := make([]map[string]any, 0, len(steps))
	for _, s := range steps {
		out = append(out, map[string]any{"color": s[0], "value": s[1]})
	}
	return map[string]any{"mode": "absolute", "steps": out}
}

func row(title string, y int) map[string]any {
	return map[string]any{
		"type": "row", "title": title, "collapsed": false,
		"gridPos": gp(0, y, 24, 1), "panels": []any{},
	}
}

// stat is a single-value panel.
func stat(title string, g map[string]any, tgts []map[string]any, unit, desc string, th map[string]any) map[string]any {
	if th == nil {
		th = thresholds([2]any{"green", nil})
	}
	return map[string]any{
		"type": "stat", "title": title, "description": desc, "datasource": ds(), "gridPos": g,
		"targets": tgts,
		"fieldConfig": map[string]any{
			"defaults":  map[string]any{"unit": unit, "color": map[string]any{"mode": "thresholds"}, "thresholds": th},
			"overrides": []any{},
		},
		"options": map[string]any{
			"reduceOptions": map[string]any{"calcs": []string{"lastNotNull"}},
			"colorMode":     "value", "graphMode": "area", "textMode": "auto",
		},
	}
}

// gauge is a 0..max gauge panel with green/yellow/red thresholds.
func gauge(title string, g map[string]any, tgts []map[string]any, desc string) map[string]any {
	return map[string]any{
		"type": "gauge", "title": title, "description": desc, "datasource": ds(), "gridPos": g,
		"targets": tgts,
		"fieldConfig": map[string]any{
			"defaults": map[string]any{
				"unit": "percent", "min": 0, "max": 100, "decimals": 1,
				"color":      map[string]any{"mode": "thresholds"},
				"thresholds": thresholds([2]any{"green", nil}, [2]any{"yellow", 70}, [2]any{"red", 90}),
			},
			"overrides": []any{},
		},
		"options": map[string]any{
			"reduceOptions":        map[string]any{"calcs": []string{"lastNotNull"}},
			"showThresholdMarkers": true,
		},
	}
}

// ts is a timeseries panel.
func ts(title string, g map[string]any, tgts []map[string]any, unit string) map[string]any {
	return map[string]any{
		"type": "timeseries", "title": title, "datasource": ds(), "gridPos": g,
		"targets": tgts,
		"fieldConfig": map[string]any{
			"defaults": map[string]any{
				"unit":  unit,
				"color": map[string]any{"mode": "palette-classic"},
				"custom": map[string]any{
					"drawStyle": "line", "lineInterpolation": "smooth", "lineWidth": 1,
					"fillOpacity": 10, "showPoints": "never", "spanNulls": true,
				},
			},
			"overrides": []any{},
		},
		"options": map[string]any{
			"legend":  map[string]any{"displayMode": "table", "placement": "bottom", "calcs": []string{"mean", "lastNotNull", "max"}},
			"tooltip": map[string]any{"mode": "multi", "sort": "desc"},
		},
	}
}

// queryVar is a label_values template variable (multi-select, defaults to All)
// whose variable name matches the Prometheus label it enumerates.
func queryVar(name, label, metric, matcher string) map[string]any {
	return queryVarOn(name, label, name, metric, matcher)
}

// queryVarOn is queryVar for the case where the variable name differs from the
// Prometheus label it enumerates (e.g. a var named "cluster" extracting the
// promoted "db_cluster_name" label). matcher may reference an earlier variable
// (e.g. db_cluster_name=~"$cluster") to chain one selector to another.
func queryVarOn(name, label, promLabel, metric, matcher string) map[string]any {
	q := fmt.Sprintf("label_values(%s{%s}, %s)", metric, matcher, promLabel)
	return map[string]any{
		"name": name, "type": "query", "label": label, "datasource": ds(),
		"query": map[string]any{"query": q, "refId": name + "Var"}, "definition": q,
		"includeAll": true, "allValue": ".*", "multi": true,
		"current": map[string]any{"text": []string{"All"}, "value": []string{"$__all"}},
		"refresh": 2, "sort": 1,
	}
}

func dashboard(title, uid string, tags []string, vars []map[string]any, panels []map[string]any) (string, error) {
	if vars == nil {
		vars = []map[string]any{}
	}
	d := map[string]any{
		"uid": uid, "title": title, "tags": tags, "timezone": "browser",
		"schemaVersion": schemaVersion, "version": 1, "refresh": "1m", "editable": true,
		"time":        map[string]any{"from": "now-6h", "to": "now"},
		"templating":  map[string]any{"list": vars},
		"annotations": map[string]any{"list": []any{}},
		"panels":      panels,
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("grafanadash: marshal %s: %w", uid, err)
	}
	return string(b), nil
}

// envMatcher is the per-env label selector every query carries so a shared Grafana
// org shows only this env's series (deployment.environment.name == identity env).
func envMatcher(env string) string {
	return fmt.Sprintf(`deployment_environment_name=%q`, env)
}
