package validate

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/wardnet/inforge/internal/grafanaalert"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/types"
)

// pagerDutySeverities are the urgencies a PagerDuty integration may declare.
var pagerDutySeverities = map[string]bool{"critical": true, "error": true, "warning": true, "info": true}

// checkObservability validates the env's alert + notification authoring
// credential-free (ADR-0038 slice 3): contact-point shapes, profile routing
// completeness, custom-alert specs, and the cross-file default_profile. It reads
// only the plaintext structure — no decryption keys needed. It is a no-op when the
// files are absent and nothing is managed.
func checkObservability(r *reporter, dir, env string, obs types.ObservabilityConfig) {
	notif, err := loader.LoadNotifications(env, dir)
	notifPath := filepath.Join(dir, env, "observability", "notifications.yaml")
	if err != nil {
		r.fail(notifPath, err.Error())
		return
	}
	alerts, err := loader.LoadAlerts(env, dir)
	alertsPath := filepath.Join(dir, env, "observability", "alerts.yaml")
	if err != nil {
		r.fail(alertsPath, err.Error())
		return
	}

	// Alerts are managed when Grafana is configured and either the built-ins are on
	// or the env authored custom alerts. Only then are default_profile + routing
	// completeness load-bearing.
	alertsManaged := obs.GrafanaURL != "" && (obs.AlertsEnabled() || len(alerts.Alerts) > 0)

	var nerrs []string

	// Contact points: exactly one integration, and its required field is set.
	for _, name := range sortedKeys(notif.ContactPoints) {
		cp := notif.ContactPoints[name]
		set := 0
		if cp.PagerDuty != nil {
			set++
			if cp.PagerDuty.KeySecret == "" {
				nerrs = append(nerrs, fmt.Sprintf("contact_points.%s.pagerduty: key_secret is required", name))
			}
			if s := cp.PagerDuty.Severity; s != "" && !pagerDutySeverities[s] {
				nerrs = append(nerrs, fmt.Sprintf("contact_points.%s.pagerduty.severity %q: want critical|error|warning|info", name, s))
			}
		}
		if cp.Email != nil {
			set++
			if len(cp.Email.Addresses) == 0 {
				nerrs = append(nerrs, fmt.Sprintf("contact_points.%s.email: at least one address is required", name))
			}
		}
		if cp.Webhook != nil {
			set++
			if cp.Webhook.URL == "" {
				nerrs = append(nerrs, fmt.Sprintf("contact_points.%s.webhook: url is required", name))
			}
		}
		if set != 1 {
			nerrs = append(nerrs, fmt.Sprintf("contact_points.%s: exactly one integration (pagerduty|email|webhook) must be set, found %d", name, set))
		}
	}

	// Profiles: valid severities, exactly one of contact_point|muted, and every
	// referenced contact point exists.
	for _, pname := range sortedKeys(notif.Profiles) {
		for _, sev := range sortedKeys(notif.Profiles[pname]) {
			route := notif.Profiles[pname][sev]
			if !grafanaalert.Severities[sev] {
				nerrs = append(nerrs, fmt.Sprintf("profiles.%s.%s: invalid severity (want critical|warning|info)", pname, sev))
			}
			hasCP := route.ContactPoint != ""
			if hasCP == route.Muted {
				nerrs = append(nerrs, fmt.Sprintf("profiles.%s.%s: set exactly one of contact_point or muted: true", pname, sev))
			}
			if hasCP {
				if _, ok := notif.ContactPoints[route.ContactPoint]; !ok {
					nerrs = append(nerrs, fmt.Sprintf("profiles.%s.%s: contact_point %q is not defined in contact_points", pname, sev, route.ContactPoint))
				}
			}
		}
	}

	// default_profile: required (and must exist + route the built-in severities)
	// once alerts are managed.
	if alertsManaged {
		if obs.DefaultProfile == "" {
			nerrs = append(nerrs, "observability.default_profile is required in variables.yaml once alerts are managed (set built_in_alerts: false and author no alerts to disable)")
		} else if _, ok := notif.Profiles[obs.DefaultProfile]; !ok {
			nerrs = append(nerrs, fmt.Sprintf("observability.default_profile %q is not defined in notifications.yaml profiles", obs.DefaultProfile))
		} else if obs.AlertsEnabled() {
			// Built-in alerts fire at critical + warning through the default profile.
			for _, sev := range []string{grafanaalert.SeverityCritical, grafanaalert.SeverityWarning} {
				if _, ok := notif.Profiles[obs.DefaultProfile][sev]; !ok {
					nerrs = append(nerrs, fmt.Sprintf("profiles.%s.%s: default_profile must route %q (built-in alerts use it) — add a contact_point or muted: true", obs.DefaultProfile, sev, sev))
				}
			}
		}
	}
	if len(nerrs) > 0 {
		r.report(notifPath, nerrs, nil)
	}

	// Custom alerts.
	var aerrs []string
	seen := map[string]bool{}
	for _, a := range alerts.Alerts {
		if a.Name == "" {
			aerrs = append(aerrs, "alert: name is required")
			continue
		}
		if seen[a.Name] {
			aerrs = append(aerrs, fmt.Sprintf("alert %q: duplicate name", a.Name))
		}
		seen[a.Name] = true
		if a.Expr == "" {
			aerrs = append(aerrs, fmt.Sprintf("alert %q: expr is required", a.Name))
		}
		if err := grafanaalert.ValidCondition(a.Condition); err != nil {
			aerrs = append(aerrs, fmt.Sprintf("alert %q: %v", a.Name, err))
		}
		if !grafanaalert.Severities[a.Severity] {
			aerrs = append(aerrs, fmt.Sprintf("alert %q: invalid severity %q (want critical|warning|info)", a.Name, a.Severity))
		}
		if _, ok := a.Labels["severity"]; ok {
			aerrs = append(aerrs, fmt.Sprintf("alert %q: label \"severity\" is reserved (set the alert's severity field)", a.Name))
		}
		// Routing: the alert's profile (or the env default) must route its severity.
		if obs.GrafanaURL != "" && grafanaalert.Severities[a.Severity] {
			profile := a.Profile
			if profile == "" {
				profile = obs.DefaultProfile
			}
			if profile == "" {
				aerrs = append(aerrs, fmt.Sprintf("alert %q: no profile and no observability.default_profile set", a.Name))
			} else if p, ok := notif.Profiles[profile]; !ok {
				aerrs = append(aerrs, fmt.Sprintf("alert %q: profile %q is not defined in notifications.yaml", a.Name, profile))
			} else if _, ok := p[a.Severity]; !ok {
				aerrs = append(aerrs, fmt.Sprintf("alert %q: profile %q does not route severity %q — add a contact_point or muted: true", a.Name, profile, a.Severity))
			}
		}
	}
	if len(aerrs) > 0 {
		r.report(alertsPath, aerrs, nil)
	}
}

// sortedKeys returns a map's keys sorted, for deterministic diagnostics.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
