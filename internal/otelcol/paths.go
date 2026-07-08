// Package otelcol renders the host VM-metrics collector's config and the
// idempotent shell that installs the off-the-shelf OpenTelemetry Collector Contrib
// .deb on a VM (ADR-0031). It is deploy-side only (never imported by
// inforge-agent) and holds no provider/Pulumi dependencies — the program wires
// it to hosts, exactly as internal/nginx is wired to ingress hosts.
package otelcol

import "fmt"

// On-host names the install + provisioning paths must agree on. They are fixed by
// the official otelcol-contrib .deb (verified against the packaging at v0.155.0):
// the unit reads EnvironmentFile /etc/otelcol-contrib/otelcol-contrib.conf whose
// default OTELCOL_OPTIONS points ExecStart at ConfigPath, and runs as the
// unprivileged User created by the package postinstall.
const (
	// Package is the dpkg package name the .deb installs.
	Package = "otelcol-contrib"
	// Service is the systemd unit the .deb ships and enables.
	Service = "otelcol-contrib.service"
	// User/Group is the unprivileged account the unit runs as. The credential file
	// is chown'd to it so only the collector can read it.
	User = "otelcol-contrib"
	// ConfigPath is the config the unit loads by default; we overwrite it.
	ConfigPath = "/etc/otelcol-contrib/config.yaml"
	// CredentialPath holds the base64 OTLP Basic-auth value (0600, owned by User),
	// referenced from the rendered config via the collector's ${file:…} provider so
	// the secret never appears in the config itself.
	CredentialPath = "/etc/otelcol-contrib/otlp-auth"
	// DefaultVersion is the otelcol-contrib release installed when an env does not
	// pin one. Bump deliberately (a new .deb is downloaded + apt-installed on next
	// deploy).
	DefaultVersion = "0.155.0"
)

// AuthSecretNamespace / AuthSecretKey locate the OTLP Basic-auth credential in the
// env's secrets.enc.yaml. It is an inforge RESERVED secret (ADR-0031), not a
// service container secret: it lives under the store's `reserved:` namespace,
// referenced by no service and read directly by the deploy — so a user service
// may use the container name "observability" without colliding. The stored value
// is the raw "instanceID:token" Grafana Cloud hands out for OTLP Basic auth;
// inforge base64-encodes it at deploy and writes the result to CredentialPath.
const (
	AuthSecretNamespace = "observability"
	AuthSecretKey       = "otlp_auth"
)

// MonitorPasswordPath is the on-host file holding a Postgres cluster's monitoring
// role password (ADR-0037), read by that cluster's postgresql receiver via the
// collector's ${file:…} provider. Written 0600 owned by User, one per cluster (a
// host may run several). cluster is an inforge-derived name, safe in a path.
func MonitorPasswordPath(cluster string) string {
	return "/etc/otelcol-contrib/pg-" + cluster
}

// DebAsset is the release asset filename for a version+arch, e.g.
// "otelcol-contrib_0.155.0_linux_amd64.deb". version carries no leading "v".
func DebAsset(version, arch string) string {
	return fmt.Sprintf("%s_%s_linux_%s.deb", Package, version, arch)
}

// DownloadBaseURL is the GitHub release download base for a version (tag has the
// leading "v"; the asset name does not).
func DownloadBaseURL(version string) string {
	return fmt.Sprintf("https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v%s", version)
}

// ChecksumsAsset is the release checksums file the install step verifies against.
const ChecksumsAsset = "opentelemetry-collector-releases_otelcol-contrib_checksums.txt"
