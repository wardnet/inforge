// Package grafanaalert renders inforge's alert rules (ADR-0038 slice 3) into a
// provider-agnostic model the Pulumi deploy path turns into
// grafana.alerting.RuleGroup resources. Like internal/grafanadash and
// internal/otelcol it is pure and Pulumi-free: it builds the query/expression node
// JSON as nested maps and returns plain structs, so program/ just wraps them in the
// SDK's Input types.
//
// Each alert compiles to the canonical three-node Grafana rule: a Prometheus query
// (A), a reduce-to-last (B), and a math threshold (C) that is the alert condition.
// The math node — rather than Grafana's threshold node, whose evaluator supports
// only gt/lt — is what lets the simplified `condition` support all of > < >= <=.
// Multi-series exprs (e.g. `... by (instance)`) fire per series, and their labels
// flow to the notification via the summary's {{ $labels.x }} templating.
package grafanaalert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/wardnet/inforge/internal/types"
)

// Severity levels, the fixed enum an alert (built-in or custom) carries. They are
// also the keys a notification profile routes on.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// Severities is the valid severity set (validation + membership checks).
var Severities = map[string]bool{SeverityCritical: true, SeverityWarning: true, SeverityInfo: true}

// Grafana rule NoData handling: most rules resolve to NoData (benign — a metric
// with no series yet), but the "metrics missing" built-ins treat absence itself as
// the alert and escalate NoData to Alerting.
const (
	NoDataOK       = "NoData"
	NoDataAlerting = "Alerting"
)

// promUID is the Grafana Cloud hosted-Prometheus datasource UID (shared with
// internal/grafanadash.PromDatasourceUID). A later slice can make it configurable.
const promUID = "grafanacloud-prom"

// exprUID is the built-in Grafana server-side expression datasource.
const exprUID = "__expr__"

// Alert bundles a compiled rule with the routing metadata program/ needs to resolve
// its contact point. Profile empty ⇒ the env's default_profile.
type Alert struct {
	Rule     Rule
	Severity string
	Profile  string
}

// Rule is the provider-agnostic eval model for one Grafana alert rule.
type Rule struct {
	Name        string
	For         string
	Condition   string // refId of the node that decides firing (always "C")
	NoDataState string
	Labels      map[string]string
	Annotations map[string]string
	Data        []RuleData
}

// RuleData is one query/expression stage. Model is the opaque JSON blob Grafana's
// rule API expects for that node.
type RuleData struct {
	RefID         string
	DatasourceUID string
	Model         string
	From          int
	To            int
}

// envMatcher is the per-env label selector every built-in query carries so a shared
// Grafana org evaluates only this env's series (mirrors grafanadash.envMatcher).
func envMatcher(env string) string {
	return fmt.Sprintf(`deployment_environment_name=%q`, env)
}

// parseCondition splits a condition like "> 90" or "<=0.01" into a Grafana math
// operator and the threshold literal. Supported operators: > < >= <=.
func parseCondition(cond string) (op, value string, err error) {
	s := strings.TrimSpace(cond)
	for _, o := range []string{">=", "<=", ">", "<"} { // longest first
		if strings.HasPrefix(s, o) {
			v := strings.TrimSpace(strings.TrimPrefix(s, o))
			if v == "" {
				return "", "", fmt.Errorf("condition %q: missing threshold value", cond)
			}
			if _, ferr := strconv.ParseFloat(v, 64); ferr != nil {
				return "", "", fmt.Errorf("condition %q: threshold %q is not a number", cond, v)
			}
			return o, v, nil
		}
	}
	return "", "", fmt.Errorf("condition %q: must start with one of > < >= <=", cond)
}

// ValidCondition reports whether a condition string is a supported threshold
// (operator > < >= <= followed by a number). Used by the credential-free validator.
func ValidCondition(cond string) error {
	_, _, err := parseCondition(cond)
	return err
}

// mustJSON marshals a node model with HTML escaping OFF, so the math operators
// (> < >= <=) appear literally rather than as > etc. (Grafana would decode
// either, but the literal form is readable and diff-stable). Keys are sorted, so the
// output is deterministic.
func mustJSON(m map[string]any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(m) // map[string]any of strings/numbers/bools never fails
	return strings.TrimRight(buf.String(), "\n")
}

// dataNodes builds the canonical A(query) → B(reduce) → C(math) stages for a PromQL
// expr and a parsed threshold. The returned Condition refId is always "C".
func dataNodes(expr, op, value string) []RuleData {
	queryModel := map[string]any{
		"refId": "A", "expr": expr, "instant": false, "range": true,
		"intervalMs": 15000, "maxDataPoints": 43200,
		"datasource": map[string]any{"type": "prometheus", "uid": promUID},
	}
	reduceModel := map[string]any{
		"refId": "B", "type": "reduce", "expression": "A", "reducer": "last",
		"settings":   map[string]any{"mode": "dropNN"},
		"datasource": map[string]any{"type": "__expr__", "uid": exprUID},
	}
	mathModel := map[string]any{
		"refId": "C", "type": "math", "expression": fmt.Sprintf("$B %s %s", op, value),
		"datasource": map[string]any{"type": "__expr__", "uid": exprUID},
	}
	return []RuleData{
		{RefID: "A", DatasourceUID: promUID, Model: mustJSON(queryModel), From: 600, To: 0},
		{RefID: "B", DatasourceUID: exprUID, Model: mustJSON(reduceModel), From: 0, To: 0},
		{RefID: "C", DatasourceUID: exprUID, Model: mustJSON(mathModel), From: 0, To: 0},
	}
}

// build assembles a Rule from its parts, applying defaults (For "0", NoData OK) and
// stamping the severity label so notification routing and grouping can match on it.
func build(name, expr, cond, forDur, severity, summary, noData string, labels map[string]string) (Rule, error) {
	op, value, err := parseCondition(cond)
	if err != nil {
		return Rule{}, fmt.Errorf("alert %q: %w", name, err)
	}
	if forDur == "" {
		forDur = "0"
	}
	if noData == "" {
		noData = NoDataOK
	}
	lbls := map[string]string{"severity": severity}
	for k, v := range labels {
		lbls[k] = v
	}
	ann := map[string]string{}
	if summary != "" {
		ann["summary"] = summary
	}
	return Rule{
		Name: name, For: forDur, Condition: "C", NoDataState: noData,
		Labels: lbls, Annotations: ann, Data: dataNodes(expr, op, value),
	}, nil
}

// Custom compiles the env's authored alert specs into Alerts. It is credential- and
// provider-independent; program/ resolves each Alert's contact point afterward.
func Custom(specs []types.AlertSpec) ([]Alert, error) {
	out := make([]Alert, 0, len(specs))
	for _, s := range specs {
		if !Severities[s.Severity] {
			return nil, fmt.Errorf("alert %q: invalid severity %q (want critical|warning|info)", s.Name, s.Severity)
		}
		r, err := build(s.Name, s.Expr, s.Condition, s.For, s.Severity, s.Summary, NoDataOK, s.Labels)
		if err != nil {
			return nil, err
		}
		out = append(out, Alert{Rule: r, Severity: s.Severity, Profile: s.Profile})
	}
	return out, nil
}
