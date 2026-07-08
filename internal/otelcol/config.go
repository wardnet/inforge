package otelcol

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ServiceName is the OTel service.name stamped on host metrics. It is fixed (host
// metrics are one logical "service"/Prometheus job per fleet) and distinct from any
// app service; correlation with app telemetry is on host.id, not service.name. Each
// host's identity within that job rides on service.instance.id (== HostID, see
// Attributes), which is what the Prometheus/OTel translation surfaces as the
// `instance` label — without it every host would collide under one job with no
// per-target label, forcing a target_info join just to tell hosts apart.
const ServiceName = "wardnet-host-metrics"

// DBServiceName is the OTel service.name (Prometheus job) stamped on PostgreSQL
// metrics from the postgresql receiver (ADR-0037). It is a DISTINCT job from host
// metrics so DB dashboards/alerts scope cleanly; the two still correlate on host.id.
// Within the job, per-node identity rides on service.instance.id (== HostID) and the
// cluster on db.cluster.name — so metrics filter global → per-cluster → per-node, the
// last of which becomes meaningful once a cluster spans multiple hosts.
const DBServiceName = "wardnet-db-metrics"

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

// PostgresTarget describes one self-hosted Postgres cluster the collector scrapes
// via the postgresql receiver (ADR-0037). The receiver connects to the cluster's
// LOCAL instance over loopback (the collector runs on the cluster host); Password is
// read from PasswordFile via the collector's ${file:…} provider, never inlined.
type PostgresTarget struct {
	Cluster      string   // cluster name → db.cluster.name + the receiver/pipeline key
	Port         int      // the cluster's local TCP port (scraped at 127.0.0.1:Port)
	Username     string   // the pg_monitor monitoring role
	PasswordFile string   // on-host file holding the role password (0600, otelcol-contrib)
	Databases    []string // databases to scrape (metrics-enabled; ≥1 required)
}

func (t PostgresTarget) validate() error {
	switch {
	case t.Cluster == "":
		return fmt.Errorf("otelcol: postgres target with empty cluster")
	case t.Port == 0:
		return fmt.Errorf("otelcol: postgres target %q with no port", t.Cluster)
	case t.Username == "":
		return fmt.Errorf("otelcol: postgres target %q with empty username", t.Cluster)
	case t.PasswordFile == "":
		return fmt.Errorf("otelcol: postgres target %q with empty password file", t.Cluster)
	case len(t.Databases) == 0:
		return fmt.Errorf("otelcol: postgres target %q with no databases", t.Cluster)
	}
	return nil
}

// identityAttrs returns the shared ADR-0030 resource identity (host.id + cloud/host
// facets), best-effort — an empty field is omitted. It is stamped on BOTH host and
// DB metrics so the two correlate on host.id. service.name / service.instance.id /
// db.cluster.name are added by the caller (they differ per signal).
func identityAttrs(attrs Attributes) []map[string]any {
	out := []map[string]any{}
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
			out = append(out, upsert(kv[0], kv[1]))
		}
	}
	return out
}

// Render builds the collector config: a hostmetrics receiver + a postgresql receiver
// per pg target, resource processors stamping attrs, and an otlphttp exporter to
// endpoint authenticated with the Basic-auth value read from CredentialPath via the
// collector's ${file:…} provider (so the secret is never in the config text).
// endpoint and attrs.HostID are required. With pg empty the config is byte-identical
// to a host-metrics-only collector. Each pg target gets its own receiver + metrics
// pipeline whose resource processor stamps service.name=DBServiceName,
// service.instance.id=HostID, db.cluster.name=<cluster>, plus the shared identity.
func Render(endpoint string, attrs Attributes, pg []PostgresTarget) (string, error) {
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

	// service.name is fixed fleet-wide (one logical "job"); service.instance.id is
	// the per-host identity Prometheus/OTel translation maps onto the `instance`
	// label, so hosts stay distinguishable on that label without a target_info join.
	// Both are always present; the shared identity is appended (empty fields omitted).
	hostResourceAttrs := append([]map[string]any{
		upsert("service.name", ServiceName),
		upsert("service.instance.id", attrs.HostID),
	}, identityAttrs(attrs)...)

	receivers := map[string]any{
		"hostmetrics": map[string]any{
			"collection_interval": CollectionInterval,
			"scrapers":            scrapers,
		},
	}
	processors := map[string]any{
		"resource": map[string]any{"attributes": hostResourceAttrs},
		"batch":    map[string]any{},
	}
	pipelines := map[string]any{
		"metrics": map[string]any{
			"receivers":  []string{"hostmetrics"},
			"processors": []string{"resource", "batch"},
			"exporters":  []string{"otlphttp"},
		},
	}

	// One postgresql receiver + metrics pipeline per cluster. Each carries its own
	// resource processor: the DB job name, the per-node instance id, and the cluster
	// name — so metrics filter global (job) → per-cluster (db.cluster.name) → per-node
	// (service.instance.id). Per-database drill-down is free (the receiver stamps
	// postgresql.database.name itself).
	for _, t := range pg {
		if err := t.validate(); err != nil {
			return "", err
		}
		recName := "postgresql/" + t.Cluster
		procName := "resource/db-" + t.Cluster
		pipeName := "metrics/db-" + t.Cluster

		receivers[recName] = map[string]any{
			"endpoint":            fmt.Sprintf("127.0.0.1:%d", t.Port),
			"username":            t.Username,
			"password":            fmt.Sprintf("${file:%s}", t.PasswordFile),
			"databases":           t.Databases,
			"tls":                 map[string]any{"insecure": true},
			"collection_interval": CollectionInterval,
		}
		dbResourceAttrs := append([]map[string]any{
			upsert("service.name", DBServiceName),
			upsert("service.instance.id", attrs.HostID),
			upsert("db.cluster.name", t.Cluster),
		}, identityAttrs(attrs)...)
		processors[procName] = map[string]any{"attributes": dbResourceAttrs}
		pipelines[pipeName] = map[string]any{
			"receivers":  []string{recName},
			"processors": []string{procName, "batch"},
			"exporters":  []string{"otlphttp"},
		}
	}

	cfg := map[string]any{
		"receivers":  receivers,
		"processors": processors,
		"exporters": map[string]any{
			"otlphttp": map[string]any{
				"endpoint": endpoint,
				"headers": map[string]any{
					"Authorization": fmt.Sprintf("Basic ${file:%s}", CredentialPath),
				},
			},
		},
		"service": map[string]any{"pipelines": pipelines},
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
