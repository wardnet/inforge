package postgres

import (
	"fmt"
	"strings"
)

// InstallScript renders the idempotent shell that installs the PGDG Postgres server
// package for the given major version on a host (ADR-0036). It disables the Debian
// auto-created "main" cluster (inforge manages its own clusters at explicit data dirs
// and ports), adds the PGDG apt repo once, and installs postgresql-<version> only when
// absent. Version-pinned like the otelcol install; a major bump installs a new package
// on the next deploy.
func InstallScript(version string) string {
	ver := resolveVersion(version)
	return strings.Join([]string{
		"set -e",
		// Suppress the package's default 'main' cluster before install.
		"sudo install -d -m 0755 /etc/postgresql-common",
		`printf 'create_main_cluster = false\n' | sudo tee /etc/postgresql-common/createcluster.conf >/dev/null`,
		// PGDG apt repo (added once).
		"if [ ! -f /etc/apt/sources.list.d/pgdg.list ]; then",
		"  sudo install -d -m 0755 /usr/share/keyrings",
		"  curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc | sudo gpg --dearmor -o /usr/share/keyrings/pgdg.gpg",
		"  . /etc/os-release",
		`  echo "deb [signed-by=/usr/share/keyrings/pgdg.gpg] https://apt.postgresql.org/pub/repos/apt ${VERSION_CODENAME}-pgdg main" | sudo tee /etc/apt/sources.list.d/pgdg.list >/dev/null`,
		"  sudo apt-get update",
		"fi",
		// Install the server package only when not already installed.
		fmt.Sprintf(`if ! dpkg-query -W -f='${Status}' postgresql-%s 2>/dev/null | grep -q "install ok installed"; then`, ver),
		fmt.Sprintf(`  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y postgresql-%s`, ver),
		"fi",
	}, "\n")
}

// MountScript renders the idempotent shell that prepares a cluster's persistent data
// volume: format it ext4 only if it carries no filesystem (never reformat existing
// data — the durability guarantee), add an fstab entry keyed by UUID, and mount it at
// the cluster's MountDir. device is the attached volume's block-device path (supplied
// by the volume primitive).
func MountScript(device, cluster string) string {
	mnt := MountDir(cluster)
	return strings.Join([]string{
		"set -e",
		"dev=" + shQuote(device),
		"mnt=" + shQuote(mnt),
		`sudo install -d -m 0755 "$mnt"`,
		// Format ONLY when the device has no filesystem — protects existing data.
		`if ! sudo blkid "$dev" >/dev/null 2>&1; then`,
		`  sudo mkfs.ext4 -F "$dev"`,
		"fi",
		`uuid=$(sudo blkid -s UUID -o value "$dev")`,
		`[ -n "$uuid" ] || { echo "no UUID for $dev after mkfs" >&2; exit 1; }`,
		`if ! grep -q "$uuid" /etc/fstab; then`,
		`  echo "UUID=$uuid $mnt ext4 defaults,noatime 0 2" | sudo tee -a /etc/fstab >/dev/null`,
		"fi",
		`mountpoint -q "$mnt" || sudo mount "$mnt"`,
	}, "\n")
}

// InitClusterScript renders the idempotent shell that runs initdb into the cluster's
// PGDATA (on the mounted volume) if it is not yet initialized. The data dir is created
// owned by the postgres user and empty so initdb accepts it; a re-run with an existing
// cluster (PG_VERSION present) is a no-op, so reattaching a populated volume never
// re-inits.
func InitClusterScript(cluster, version string) string {
	data := DataDir(cluster)
	bin := BinDir(version)
	return strings.Join([]string{
		"set -e",
		"data=" + shQuote(data),
		`if [ ! -f "$data/PG_VERSION" ]; then`,
		fmt.Sprintf(`  sudo install -d -o %s -g %s -m 0700 "$data"`, OSUser, OSUser),
		fmt.Sprintf(`  sudo -u %s %s/initdb -D "$data" --auth-local=peer --auth-host=scram-sha-256 --encoding=UTF8`, OSUser, bin),
		"fi",
	}, "\n")
}

// UnitFile renders the systemd unit for a cluster's postmaster: it runs as the
// postgres user against the cluster's PGDATA on the given port, and requires the data
// volume to be mounted first (RequiresMountsFor) so it never starts against an
// unmounted directory.
func UnitFile(cluster, version string, port int) string {
	return strings.Join([]string{
		"[Unit]",
		fmt.Sprintf("Description=wardnet Postgres cluster %s (inforge, ADR-0036)", cluster),
		"After=network-online.target",
		"Wants=network-online.target",
		fmt.Sprintf("RequiresMountsFor=%s", MountDir(cluster)),
		"",
		"[Service]",
		"Type=notify",
		fmt.Sprintf("User=%s", OSUser),
		fmt.Sprintf("Group=%s", OSUser),
		fmt.Sprintf("ExecStart=%s/postgres -D %s -p %d", BinDir(version), DataDir(cluster), port),
		"ExecReload=/bin/kill -HUP $MAINPID",
		"KillSignal=SIGINT",
		"TimeoutStartSec=120",
		"Restart=on-failure",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n")
}

// ApplyScript writes the cluster's config files into its PGDATA (owned by postgres,
// 0600) and installs + (re)starts the systemd unit. It assumes the volume is mounted
// and the cluster initialized (MountScript + InitClusterScript ran first).
func ApplyScript(c ClusterConfig) (string, error) {
	conf, err := RenderPostgresqlConf(c)
	if err != nil {
		return "", err
	}
	hba, err := RenderHBA(c)
	if err != nil {
		return "", err
	}
	data := DataDir(c.Cluster)
	owner := OSUser + ":" + OSUser
	return strings.Join([]string{
		"set -e",
		writeFileScript(data+"/postgresql.conf", conf, "0600", owner),
		writeFileScript(data+"/pg_hba.conf", hba, "0600", owner),
		writeFileScript(UnitPath(c.Cluster), UnitFile(c.Cluster, c.Version, c.Port), "0644", "root:root"),
		"sudo systemctl daemon-reload",
		fmt.Sprintf("sudo systemctl enable %s", shQuote(ServiceName(c.Cluster))),
		fmt.Sprintf("sudo systemctl restart %s", shQuote(ServiceName(c.Cluster))),
	}, "\n"), nil
}
