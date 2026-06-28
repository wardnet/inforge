package otelcol

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ServiceName is the OTel service.name stamped on host metrics. It is fixed (host
// metrics are one logical "service" per fleet) and distinct from any app service;
// correlation with app telemetry is on host.id, not service.name.
const ServiceName = "wardnet-host-metrics"

// CollectionInterval is how often the hostmetrics receiver scrapes.
const CollectionInterval = "60s"

// hostScrapers are the host-level signals collected. Each reads world-readable
// /proc or /sys, so the collector needs no privilege (ADR-0031). The `process`
// scraper — per-process inventory across all users, which needs root or
// CAP_SYS_PTRACE — is deliberately omitted; adding it is a separate opt-in.
var hostScrapers = []string{"cpu", "load", "memory", "disk", "filesystem", "network", "paging"}

// Attributes is the resource identity stamped on every host metric. It is the same
// set inforge injects into app telemetry (ADR-0030) so host metrics and app
// telemetry correlate on host.id. HostID is required; the rest are best-effort —
// an empty field is omitted (a provider that did not supply it stamps nothing).
type Attributes struct {
	HostID           string // host.id
	CloudProvider    string // cloud.provider
	CloudRegion      string // cloud.region
	AvailabilityZone string // cloud.availability_zone
	MachineType      string // host.type
	Environment      string // deployment.environment.name
	RegionSlug       string // region
}

// Render builds the collector config: a hostmetrics receiver, a resource processor
// stamping attrs, and an otlphttp exporter to endpoint authenticated with the
// Basic-auth value read from CredentialPath via the collector's ${file:…} provider
// (so the secret is never in the config text). endpoint and attrs.HostID are
// required.
func Render(endpoint string, attrs Attributes) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("otelcol: empty OTLP endpoint")
	}
	if attrs.HostID == "" {
		return "", fmt.Errorf("otelcol: empty host id")
	}

	scrapers := map[string]any{}
	for _, s := range hostScrapers {
		scrapers[s] = map[string]any{}
	}

	// service.name is always present; the rest are appended only when non-empty so a
	// missing attribute is omitted rather than stamped blank.
	resourceAttrs := []map[string]any{upsert("service.name", ServiceName)}
	for _, kv := range [][2]string{
		{"host.id", attrs.HostID},
		{"host.type", attrs.MachineType},
		{"cloud.provider", attrs.CloudProvider},
		{"cloud.region", attrs.CloudRegion},
		{"cloud.availability_zone", attrs.AvailabilityZone},
		{"deployment.environment.name", attrs.Environment},
		{"region", attrs.RegionSlug},
	} {
		if kv[1] != "" {
			resourceAttrs = append(resourceAttrs, upsert(kv[0], kv[1]))
		}
	}

	cfg := map[string]any{
		"receivers": map[string]any{
			"hostmetrics": map[string]any{
				"collection_interval": CollectionInterval,
				"scrapers":            scrapers,
			},
		},
		"processors": map[string]any{
			"resource": map[string]any{"attributes": resourceAttrs},
			"batch":    map[string]any{},
		},
		"exporters": map[string]any{
			"otlphttp": map[string]any{
				"endpoint": endpoint,
				"headers": map[string]any{
					"Authorization": fmt.Sprintf("Basic ${file:%s}", CredentialPath),
				},
			},
		},
		"service": map[string]any{
			"pipelines": map[string]any{
				"metrics": map[string]any{
					"receivers":  []string{"hostmetrics"},
					"processors": []string{"resource", "batch"},
					"exporters":  []string{"otlphttp"},
				},
			},
		},
	}

	b, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("otelcol: marshal config: %w", err)
	}
	return string(b), nil
}

func upsert(key, value string) map[string]any {
	return map[string]any{"key": key, "value": value, "action": "upsert"}
}
