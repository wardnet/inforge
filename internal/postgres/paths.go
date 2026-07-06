// Package postgres renders the config and the idempotent shell that provisions a
// self-hosted Postgres database-cluster on a VM (ADR-0036): the version-pinned PGDG
// install, the per-cluster data-volume mount + initdb, the postgresql.conf /
// pg_hba.conf, and the on-host `psql` that creates the cluster's databases/owner and
// mints per-service roles. Like internal/otelcol and internal/nginx it is pure —
// deploy-side only, with no Pulumi/provider dependency; the program wires it to
// hosts. Role SQL text comes from internal/pgrole (shared with the retiring Neon
// provider); this package owns the on-host transport (psql over local peer auth),
// which a private-only cluster requires because it is unreachable from the deploy
// machine.
package postgres

const (
	// DefaultVersion is the Postgres major installed when a database-cluster does not
	// pin a version. Bump deliberately (a new PGDG package is installed on the next
	// deploy; a major upgrade of existing data is a separate, manual operation).
	DefaultVersion = "17"

	// OSUser is the unix + bootstrap-superuser account the PGDG package creates.
	// inforge runs each cluster's postmaster as it and connects locally over the unix
	// socket (peer auth) to manage roles/databases — so no superuser password ever
	// leaves the host.
	OSUser = "postgres"

	// SocketDir is the unix-socket directory (the PGDG default) used for local peer
	// connections (role minting, backups). Co-located clusters share it, keyed by port.
	SocketDir = "/var/run/postgresql"

	// PortBase is the TCP port of the first (host-sorted) cluster on a host; the Nth
	// cluster listens on PortBase+N. MaxClustersPerHost bounds the reserved range.
	PortBase           = 5432
	MaxClustersPerHost = 32

	// dataRoot is the parent of every cluster's mount point.
	dataRoot = "/var/lib/wardnet/db"
)

// MountDir is where a cluster's persistent volume is mounted. The whole PGDATA lives
// under it, so reattaching the volume to a rebuilt host recovers the cluster.
func MountDir(cluster string) string { return dataRoot + "/" + cluster }

// DataDir is the PGDATA (initdb target) — a subdirectory of the mount, so a freshly
// formatted ext4 volume's lost+found does not violate initdb's empty-directory
// requirement.
func DataDir(cluster string) string { return MountDir(cluster) + "/pgdata" }

// UnitName is the systemd unit inforge manages for the cluster's postmaster (no
// .service suffix); ServiceName adds it.
func UnitName(cluster string) string    { return "wardnet-db-" + cluster }
func ServiceName(cluster string) string { return UnitName(cluster) + ".service" }

// UnitPath is the on-host path of the cluster's systemd unit file.
func UnitPath(cluster string) string { return "/etc/systemd/system/" + ServiceName(cluster) }

// ClusterPort is the deterministic TCP port of the index-th (0-based, host-sorted)
// cluster on a host. Assignment is single-sourced and stable across deploys, like the
// mesh egress-port scheme.
func ClusterPort(index int) int { return PortBase + index }

// InReservedPortRange reports whether port falls in the range reserved for
// co-located cluster postmasters — a co-located non-Postgres listener must avoid it.
func InReservedPortRange(port int) bool {
	return port >= PortBase && port < PortBase+MaxClustersPerHost
}

// BinDir is the PGDG per-version binary directory (initdb/postgres live here).
func BinDir(version string) string { return "/usr/lib/postgresql/" + resolveVersion(version) + "/bin" }

// resolveVersion returns the pinned version or the default when unset.
func resolveVersion(version string) string {
	if version == "" {
		return DefaultVersion
	}
	return version
}
