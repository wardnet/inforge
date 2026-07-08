// Package grafana holds the small, dependency-free constants and naming the
// Grafana-integration path shares between the Pulumi deploy (dashboards + alert
// rules) and the org-level notification sync (ADR-0038). Dashboard/alert rendering
// lives in internal/grafanadash and internal/grafanaalert; this package is only the
// reserved-secret address and the env-prefixed naming, so both can agree on them
// without importing the renderers or the provider SDK.
package grafana

// ReservedNamespace is the reserved-secret namespace for Grafana credentials. It is
// the same "observability" namespace the OTLP credential uses
// (otelcol.AuthSecretNamespace): both are inforge-internal observability secrets that
// live in the store's reserved: namespace, never a service container
// (.agents/rules/reserved-secrets-live-outside-container-namespace.md).
const ReservedNamespace = "observability"

// TokenKey is the reserved-secret key holding the Grafana service-account token used
// to authenticate the Pulumi provider and the notification sync (ADR-0038). Written
// with `inforge secret set <env> observability grafana_token --reserved`.
const TokenKey = "grafana_token"

// FolderUID / FolderTitle name the per-env Grafana folder that holds this env's
// inforge-managed dashboards. Everything Grafana-side is prefixed by the identity env
// (the slug, ADR-0028) so multiple inforge envs sharing one Grafana org never collide.
func FolderUID(env string) string   { return "inforge-" + env }
func FolderTitle(env string) string { return "inforge / " + env }

// DashboardUID names one managed dashboard, prefixed by env and a stable kind slug
// ("infrastructure", "database", "service", or a custom dashboard's name).
func DashboardUID(env, kind string) string { return "inforge-" + env + "-" + kind }

// ContactPointName names one env-scoped Grafana contact point, prefixed by env so
// multiple inforge envs sharing one org never collide (ADR-0038 slice 3). The name
// is what an alert rule's NotificationSettings.ContactPoint references.
func ContactPointName(env, name string) string { return "inforge-" + env + "-" + name }

// AlertGroupName names one env-scoped Grafana alert rule group (kind is "builtin"
// or "custom"). Env-prefixed for the same shared-org reason as dashboards.
func AlertGroupName(env, kind string) string { return "inforge-" + env + "-" + kind }
