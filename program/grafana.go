package program

import (
	"fmt"
	"sort"
	"strings"

	grafanasdk "github.com/pulumiverse/pulumi-grafana/sdk/go/grafana"
	"github.com/pulumiverse/pulumi-grafana/sdk/go/grafana/alerting"
	"github.com/pulumiverse/pulumi-grafana/sdk/go/grafana/oss"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/wardnet/inforge/internal/grafana"
	"github.com/wardnet/inforge/internal/grafanaalert"
	"github.com/wardnet/inforge/internal/grafanadash"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
)

// realizeGrafana pushes this env's dashboards, alert rules, and contact points to
// Grafana via the pulumiverse/grafana provider (ADR-0038). It is org-global — created
// ONCE per run, outside the per-scope loop — and gated on observability.grafana_url:
// absent, it is a no-op. The service-account token is the reserved secret
// observability/grafana_token (grafana.TokenKey), decrypted like the OTLP credential; a
// URL set with no token is a hard misconfiguration (fail at up, skipped in preview).
// Everything is env-prefixed (folder + dashboard/contact-point/alert names) so multiple
// inforge envs share one Grafana org cleanly. Built-in dashboards and built-in alerts
// are each opt-out per env (obs.DashboardsEnabled / obs.AlertsEnabled); routing uses
// per-rule notification settings, so no org-singleton notification policy is touched.
func realizeGrafana(ctx *pulumi.Context, dir, srcEnv, env string, obs types.ObservabilityConfig, hasDatabase, dryRun bool, services []grafanaalert.ServiceScope) error {
	if obs.GrafanaURL == "" {
		return nil
	}

	tokenRaw, err := decryptReservedSecret(dir, srcEnv, grafana.ReservedNamespace, grafana.TokenKey, dryRun)
	if err != nil {
		return err
	}
	if tokenRaw == "" && !dryRun {
		return fmt.Errorf("observability: grafana_url is set but secrets.enc.yaml is missing or has no %s/%s token — run `inforge secret set %s %s %s --reserved` and commit the store", grafana.ReservedNamespace, grafana.TokenKey, srcEnv, grafana.ReservedNamespace, grafana.TokenKey)
	}

	prov, err := grafanasdk.NewProvider(ctx, "grafana", &grafanasdk.ProviderArgs{
		Url:  pulumi.String(obs.GrafanaURL),
		Auth: pulumi.ToSecret(pulumi.String(tokenRaw)).(pulumi.StringOutput),
	})
	if err != nil {
		return fmt.Errorf("observability: grafana provider: %w", err)
	}

	folder, err := oss.NewFolder(ctx, "grafana-folder", &oss.FolderArgs{
		Uid:   pulumi.String(grafana.FolderUID(env)),
		Title: pulumi.String(grafana.FolderTitle(env)),
	}, pulumi.Provider(prov))
	if err != nil {
		return fmt.Errorf("observability: grafana folder: %w", err)
	}

	newDashboard := func(name, body string) error {
		if _, err := oss.NewDashboard(ctx, "grafana-dash-"+name, &oss.DashboardArgs{
			ConfigJson: pulumi.String(body),
			Folder:     folder.Uid,
			Overwrite:  pulumi.Bool(true),
		}, pulumi.Provider(prov)); err != nil {
			return fmt.Errorf("observability: grafana dashboard %s: %w", name, err)
		}
		return nil
	}

	// Built-in dashboards, generated from the metrics inforge owns (opt-out per env).
	if obs.DashboardsEnabled() {
		builtins := []struct {
			kind   string
			render func(env, uid string) (string, error)
		}{
			{"infrastructure", grafanadash.Infrastructure},
			{"database", grafanadash.Database},
			{"service", grafanadash.Service},
		}
		for _, b := range builtins {
			body, err := b.render(env, grafana.DashboardUID(env, b.kind))
			if err != nil {
				return err
			}
			if err := newDashboard(b.kind, body); err != nil {
				return err
			}
		}
	}

	// Custom dashboards: Grafana-exported files committed under this env's
	// observability/dashboards/ directory (read from the config-source env, ADR-0028).
	// Each is normalized (parsed + env-prefixed uid) and pushed into the same folder.
	custom, err := loader.LoadCustomDashboards(srcEnv, dir)
	if err != nil {
		return err
	}
	for _, c := range custom {
		body, err := grafanadash.Custom(c.Name, grafana.DashboardUID(env, "custom-"+c.Name), c.Data, c.IsYAML)
		if err != nil {
			return err
		}
		if err := newDashboard("custom-"+c.Name, body); err != nil {
			return err
		}
	}

	// Alert rules + contact points (opt-out per env for the built-ins).
	if err := realizeGrafanaAlerts(ctx, prov, folder, dir, srcEnv, env, obs, hasDatabase, dryRun, services); err != nil {
		return err
	}
	return nil
}

// realizeGrafanaAlerts creates this env's contact points and alert rule groups
// (ADR-0038 slice 3). Contact points come from observability/notifications.yaml
// (OnCall URLs decrypted from the reserved observability namespace); rules are the
// generated built-ins (opt-out, DB ones gated on a cluster existing) plus the env's
// custom alerts. Each rule routes via its own NotificationSettings.ContactPoint —
// resolved from the alert's profile + severity — so the org notification policy is
// never touched. A route marked `muted: true` drops the rule.
func realizeGrafanaAlerts(ctx *pulumi.Context, prov *grafanasdk.Provider, folder *oss.Folder, dir, srcEnv, env string, obs types.ObservabilityConfig, hasDatabase, dryRun bool, services []grafanaalert.ServiceScope) error {
	notif, err := loader.LoadNotifications(srcEnv, dir)
	if err != nil {
		return err
	}
	alertsSpec, err := loader.LoadAlerts(srcEnv, dir)
	if err != nil {
		return err
	}

	// Nothing to manage: built-ins disabled and no custom alerts. Contact points
	// exist only to serve alerts, so skip them too (and their secret requirements).
	if !obs.AlertsEnabled() && len(alertsSpec.Alerts) == 0 {
		return nil
	}

	// Contact points, env-prefixed, created before the rule groups that name them.
	cpNames := make([]string, 0, len(notif.ContactPoints))
	for name := range notif.ContactPoints {
		cpNames = append(cpNames, name)
	}
	sort.Strings(cpNames)
	cpResources := make([]pulumi.Resource, 0, len(cpNames))
	for _, name := range cpNames {
		args, err := contactPointArgs(name, notif.ContactPoints[name], dir, srcEnv, dryRun)
		if err != nil {
			return err
		}
		args.Name = pulumi.String(grafana.ContactPointName(env, name))
		cp, err := alerting.NewContactPoint(ctx, "grafana-cp-"+name, args, pulumi.Provider(prov))
		if err != nil {
			return fmt.Errorf("observability: grafana contact point %s: %w", name, err)
		}
		cpResources = append(cpResources, cp)
	}

	// Assemble the alerts: built-ins (opt-out; DB ones gated on a cluster) + custom.
	var alerts []grafanaalert.Alert
	if obs.AlertsEnabled() {
		alerts = append(alerts, grafanaalert.BuiltIns(env, hasDatabase)...)
		alerts = append(alerts, grafanaalert.ServiceBuiltIns(env, services)...)
	}
	customAlerts, err := grafanaalert.Custom(alertsSpec.Alerts)
	if err != nil {
		return fmt.Errorf("observability: %w", err)
	}

	builtinRules, err := toRuleArgs(alerts, obs, notif, env)
	if err != nil {
		return err
	}
	customRules, err := toRuleArgs(customAlerts, obs, notif, env)
	if err != nil {
		return err
	}

	// One rule group per kind; skip an empty group. The groups depend on the contact
	// points so those exist before a rule references one by name.
	deps := pulumi.DependsOn(cpResources)
	for _, g := range []struct {
		kind  string
		rules alerting.RuleGroupRuleArray
	}{
		{"builtin", builtinRules},
		{"custom", customRules},
	} {
		if len(g.rules) == 0 {
			continue
		}
		if _, err := alerting.NewRuleGroup(ctx, "grafana-alerts-"+g.kind, &alerting.RuleGroupArgs{
			Name:            pulumi.String(grafana.AlertGroupName(env, g.kind)),
			FolderUid:       folder.Uid,
			IntervalSeconds: pulumi.Int(60),
			Rules:           g.rules,
		}, pulumi.Provider(prov), deps); err != nil {
			return fmt.Errorf("observability: grafana alert group %s: %w", g.kind, err)
		}
	}
	return nil
}

// contactPointArgs builds the provider args for one contact point, decrypting an
// OnCall integration URL from the reserved observability namespace. A missing URL
// (with grafana_url set) is a hard misconfiguration outside preview.
func contactPointArgs(name string, cp types.ContactPoint, dir, srcEnv string, dryRun bool) (*alerting.ContactPointArgs, error) {
	args := &alerting.ContactPointArgs{}
	switch {
	case cp.OnCall != nil:
		url, err := decryptReservedSecret(dir, srcEnv, grafana.ReservedNamespace, cp.OnCall.URLSecret, dryRun)
		if err != nil {
			return nil, err
		}
		if url == "" && !dryRun {
			return nil, fmt.Errorf("observability: contact point %q needs the reserved secret %s/%s — run `inforge secret set %s %s %s --reserved` and commit the store", name, grafana.ReservedNamespace, cp.OnCall.URLSecret, srcEnv, grafana.ReservedNamespace, cp.OnCall.URLSecret)
		}
		args.Oncalls = alerting.ContactPointOncallArray{
			alerting.ContactPointOncallArgs{
				Url: pulumi.ToSecret(pulumi.String(url)).(pulumi.StringOutput),
			},
		}
	case cp.Email != nil:
		addrs := make(pulumi.StringArray, 0, len(cp.Email.Addresses))
		for _, a := range cp.Email.Addresses {
			addrs = append(addrs, pulumi.String(a))
		}
		args.Emails = alerting.ContactPointEmailArray{alerting.ContactPointEmailArgs{Addresses: addrs}}
	case cp.Webhook != nil:
		args.Webhooks = alerting.ContactPointWebhookArray{alerting.ContactPointWebhookArgs{Url: pulumi.String(cp.Webhook.URL)}}
	default:
		return nil, fmt.Errorf("observability: contact point %q has no integration", name)
	}
	return args, nil
}

// toRuleArgs resolves each alert's contact point (profile+severity → notifications
// profile route) and converts the eval model into provider rule args. A muted route
// drops the alert. A route that fails to resolve is an error (validation catches this
// pre-deploy, but the deploy path stays honest).
func toRuleArgs(alerts []grafanaalert.Alert, obs types.ObservabilityConfig, notif types.NotificationsSpec, env string) (alerting.RuleGroupRuleArray, error) {
	out := alerting.RuleGroupRuleArray{}
	for _, a := range alerts {
		profile := a.Profile
		if profile == "" {
			profile = obs.DefaultProfile
		}
		if profile == "" {
			return nil, fmt.Errorf("observability: alert %q has no profile and observability.default_profile is unset", a.Rule.Name)
		}
		routes, ok := notif.Profiles[profile]
		if !ok {
			return nil, fmt.Errorf("observability: alert %q references profile %q not defined in notifications.yaml", a.Rule.Name, profile)
		}
		route, ok := routes[a.Severity]
		if !ok {
			return nil, fmt.Errorf("observability: profile %q does not route severity %q for alert %q", profile, a.Severity, a.Rule.Name)
		}
		if route.Muted {
			continue
		}

		datas := alerting.RuleGroupRuleDataArray{}
		for _, d := range a.Rule.Data {
			datas = append(datas, alerting.RuleGroupRuleDataArgs{
				RefId:         pulumi.String(d.RefID),
				DatasourceUid: pulumi.String(d.DatasourceUID),
				Model:         pulumi.String(d.Model),
				RelativeTimeRange: alerting.RuleGroupRuleDataRelativeTimeRangeArgs{
					From: pulumi.Int(d.From),
					To:   pulumi.Int(d.To),
				},
			})
		}

		labels := pulumi.StringMap{"profile": pulumi.String(profile)}
		for k, v := range a.Rule.Labels {
			labels[k] = pulumi.String(v)
		}
		annotations := pulumi.StringMap{}
		for k, v := range a.Rule.Annotations {
			annotations[k] = pulumi.String(v)
		}

		out = append(out, alerting.RuleGroupRuleArgs{
			Name:         pulumi.String(a.Rule.Name),
			Condition:    pulumi.String(a.Rule.Condition),
			For:          pulumi.String(a.Rule.For),
			NoDataState:  pulumi.String(a.Rule.NoDataState),
			ExecErrState: pulumi.String("Error"),
			Labels:       labels,
			Annotations:  annotations,
			Datas:        datas,
			NotificationSettings: alerting.RuleGroupRuleNotificationSettingsArgs{
				ContactPoint: pulumi.String(grafana.ContactPointName(env, route.ContactPoint)),
			},
		})
	}
	return out, nil
}

// serviceScopes derives each service's alert eligibility from its manifest — never from an
// authored field. inforge already knows all of this, so a `telemetry:` block on the spec
// could only be a second source of truth that drifts: set wrong, it yields either dead alert
// rules or a silently unmonitored service. Same idiom as gateway routes being derived from
// `public_paths` rather than authored.
// regionalSlugs are the region labels a REGIONAL service's instances report; a GLOBAL
// service passes nil (one instance, no region to distinguish).
//
// Liveness is generated per (service, region) because of this: a regional service runs one
// instance per region, so any expression that aggregates across regions cannot see it die in
// ONE of them — the surviving region's series keeps the aggregate alive. That is the same
// blind spot as a fleet-wide host count, moved one level down.
func serviceScopes(table regions.Table, regional, global types.Resources) []grafanaalert.ServiceScope {
	slugs := make([]string, 0, len(table))
	for name := range table {
		slug, err := table.Slug(name)
		if err != nil {
			continue // the table is validated long before here; a bad entry cannot reach us
		}
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	var out []grafanaalert.ServiceScope
	for _, set := range []struct {
		res     types.Resources
		regions []string
	}{
		{regional, slugs},
		{global, nil},
	} {
		for _, s := range set.res.Service {
			out = append(out, grafanaalert.ServiceScope{
				Name:     s.Name,
				Metric:   naming.TelemetryServiceName(s.Name),
				HTTP:     servesHTTP(s),
				Mesh:     s.Pki != "",
				Database: hasDatabaseGrant(s),
				Regions:  set.regions,
			})
		}
	}
	return out
}

// servesHTTP reports whether a service emits the RED histogram — i.e. whether it serves HTTP
// at all. A `mesh:` block means it is a mesh callee, and the mesh plane is plain HTTP. A
// tls-termination route means it is fronted by the ingress as an HTTP backend.
//
// A `forward` route does NOT count: it is L4 TLS passthrough, never terminated, so no HTTP
// request is ever seen (let alone recorded) by the service. Counting it would generate
// error-rate and latency rules against series that cannot exist.
func servesHTTP(s types.ServiceSpec) bool {
	if s.Mesh != nil && s.Mesh.Port > 0 {
		return true
	}
	for _, r := range s.Routes {
		if r.Type == types.IngressTypeTLSTermination {
			return true
		}
	}
	return false
}

// hasDatabaseGrant reports whether the service holds a connection pool that can saturate.
func hasDatabaseGrant(s types.ServiceSpec) bool {
	for _, g := range s.Grants {
		if strings.HasPrefix(g.Resource, "database/") {
			return true
		}
	}
	return false
}
