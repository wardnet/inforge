package dbbackup

import (
	"fmt"
	"strings"
	"time"
)

// BackupConfig is the input to the per-database backup renderers for one logical
// database on its cluster host.
type BackupConfig struct {
	Env         string        // environment name (identity slug) — first segment of the R2 key
	Region      string        // scope slug (region), or "global" — second segment of the R2 key
	Cluster     string        // the database-cluster resource name
	Database    string        // the logical database name (pg_dump target)
	Port        int           // the cluster's TCP port (postgres.ClusterPort)
	ClusterUnit string        // the cluster postmaster's systemd unit (After= ordering)
	Bucket      string        // destination R2 bucket name
	Endpoint    string        // R2 S3 endpoint URL (https://<acct>.r2.cloudflarestorage.com)
	Keep        int           // retention: number of newest backups to keep
	Interval    time.Duration // backup cadence (RPO)
}

func (c BackupConfig) validate() error {
	switch {
	case c.Env == "":
		return fmt.Errorf("dbbackup: empty env")
	case c.Region == "":
		return fmt.Errorf("dbbackup: empty region for database %q", c.Database)
	case c.Cluster == "":
		return fmt.Errorf("dbbackup: empty cluster name")
	case c.Database == "":
		return fmt.Errorf("dbbackup: empty database name for cluster %q", c.Cluster)
	case c.Port <= 0:
		return fmt.Errorf("dbbackup: invalid port %d for database %q", c.Port, c.Database)
	case c.Bucket == "":
		return fmt.Errorf("dbbackup: empty bucket for database %q", c.Database)
	case c.Endpoint == "":
		return fmt.Errorf("dbbackup: empty endpoint for database %q", c.Database)
	case c.Keep < 1:
		return fmt.Errorf("dbbackup: keep %d must be >= 1 for database %q", c.Keep, c.Database)
	case c.Interval <= 0:
		return fmt.Errorf("dbbackup: interval %s must be positive for database %q", c.Interval, c.Database)
	}
	return nil
}

// BackupScript renders the bash the timer's oneshot runs: dump the logical database
// (custom format, over the local unix socket as the postgres user), gzip it, and
// stream it to R2 at `<env>/<cluster>/<database>/<timestamp>.dump.gz`; then prune to
// the newest Keep objects. The ISO-8601 UTC timestamp makes the object keys sort
// lexically in chronological order, so the prune keeps the newest by sorting the key
// list and deleting all but the last Keep. `set -o pipefail` (bash) makes a failed
// pg_dump or gzip fail the whole pipe, so a broken dump never silently uploads.
func BackupScript(c BackupConfig) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	prefix := ObjectPrefix(c.Env, c.Region, c.Cluster, c.Database)
	return strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"bucket=" + shQuote(c.Bucket),
		"endpoint=" + shQuote(c.Endpoint),
		"prefix=" + shQuote(prefix),
		"ts=$(date -u +%Y%m%dT%H%M%SZ)",
		`key="${prefix}${ts}.dump.gz"`,
		// Dump as the postgres user over local peer auth (the cluster is private-only);
		// gzip; stream to R2. pipefail surfaces a pg_dump/gzip failure — the upload is the
		// critical step and stays fatal.
		fmt.Sprintf(`sudo -u postgres pg_dump -p %d -w -Fc %s | gzip | aws --endpoint-url "$endpoint" s3 cp - "s3://${bucket}/${key}"`, c.Port, shQuote(c.Database)),
		// Retention is BEST-EFFORT: the backup already uploaded, so a prune hiccup (a
		// throttled/failed delete) must not fail the unit and raise a false "backup
		// failed" alert. prune() runs with set -e suppressed (invoked under `||`); a
		// failure only logs a warning.
		"prune() {",
		"  local keys n del payload",
		// List the prefix's keys (sorted → chronological). An empty listing prints the
		// literal "None" (aws --output text), filtered so it is never treated as a key.
		`  keys=$(aws --endpoint-url "$endpoint" s3api list-objects-v2 --bucket "$bucket" --prefix "$prefix" --query 'Contents[].Key' --output text 2>/dev/null | tr '\t' '\n' | sed '/^$/d' | grep -vx None | sort)`,
		`  n=$(printf '%s\n' "$keys" | grep -c . || true)`,
		fmt.Sprintf(`  [ "${n:-0}" -gt %d ] || return 0`, c.Keep),
		fmt.Sprintf(`  del=$(( n - %d ))`, c.Keep),
		// One batched delete-objects (≤1000 keys/call) instead of a process per key. Our
		// keys are inforge-controlled (env/region/cluster/database/timestamp), so they
		// contain no characters needing JSON escaping.
		`  payload=$(printf '%s\n' "$keys" | head -n "$del" | awk 'BEGIN{printf "{\"Objects\":["} {printf "%s{\"Key\":\"%s\"}", sep, $0; sep=","} END{printf "],\"Quiet\":true}"}')`,
		`  aws --endpoint-url "$endpoint" s3api delete-objects --bucket "$bucket" --delete "$payload" >/dev/null`,
		"}",
		`prune || echo "wardnet-backup: retention prune failed (backup already uploaded)" >&2`,
		"",
	}, "\n"), nil
}

// UnitFile renders the backup oneshot's systemd unit. It reads the R2 credential from
// the shared EnvironmentFile, sets the non-secret AWS region/metadata env the R2
// upload needs, orders after the cluster postmaster, and runs the database's backup
// script.
func UnitFile(c BackupConfig) string {
	return strings.Join([]string{
		"[Unit]",
		fmt.Sprintf("Description=wardnet backup for %s/%s (inforge, ADR-0036)", c.Cluster, c.Database),
		"After=network-online.target " + c.ClusterUnit,
		"Wants=network-online.target",
		"",
		"[Service]",
		"Type=oneshot",
		"EnvironmentFile=" + CredentialPath,
		// R2 needs a region (any) and no EC2 metadata probing; both are non-secret.
		"Environment=AWS_DEFAULT_REGION=auto",
		"Environment=AWS_EC2_METADATA_DISABLED=true",
		"ExecStart=" + ScriptPath(c.Cluster, c.Database),
		"",
	}, "\n")
}

// TimerFile renders the systemd .timer that fires the backup oneshot on the database's
// cadence. OnUnitActiveSec sets the period (RPO), rendered as whole seconds (floored at
// 60s). OnBootSec is a SHORT delay (min(interval, 5m)) so the first backup after a boot
// or a fresh deploy runs promptly — not a full interval later, which would leave a new
// database with no backup for up to `interval`, and would also serve as post-downtime
// catch-up (interval timers cannot use Persistent=, which needs OnCalendar).
func TimerFile(c BackupConfig) string {
	secs := int(c.Interval / time.Second)
	if secs < 60 {
		secs = 60
	}
	boot := secs
	if boot > 300 {
		boot = 300
	}
	return strings.Join([]string{
		"[Unit]",
		fmt.Sprintf("Description=wardnet backup timer for %s/%s (inforge, ADR-0036)", c.Cluster, c.Database),
		"",
		"[Timer]",
		fmt.Sprintf("OnBootSec=%ds", boot),
		fmt.Sprintf("OnUnitActiveSec=%ds", secs),
		"AccuracySec=1min",
		"",
		"[Install]",
		"WantedBy=timers.target",
		"",
	}, "\n")
}

// ApplyScript writes the database's backup script (0755, root), its oneshot unit and
// timer (0644, root), reloads systemd, and enables + starts the timer. It assumes the
// R2 credential (CredentialScript) and awscli (InstallScript) are already in place —
// the caller orders those first.
func ApplyScript(c BackupConfig) (string, error) {
	script, err := BackupScript(c)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"set -e",
		writeFileScript(ScriptPath(c.Cluster, c.Database), script, "0755", "root:root"),
		writeFileScript(ServicePath(c.Cluster, c.Database), UnitFile(c), "0644", "root:root"),
		writeFileScript(TimerPath(c.Cluster, c.Database), TimerFile(c), "0644", "root:root"),
		"sudo systemctl daemon-reload",
		fmt.Sprintf("sudo systemctl enable --now %s", shQuote(TimerName(c.Cluster, c.Database))),
	}, "\n"), nil
}

// RemoveScript renders the teardown for a database whose backups are disabled or which
// was removed: stop + disable the timer, remove the unit/timer/script files, reload
// systemd. Idempotent — missing units are tolerated — so it is safe as a Pulumi
// resource Delete.
func RemoveScript(cluster, database string) string {
	return strings.Join([]string{
		"set -e",
		fmt.Sprintf("sudo systemctl disable --now %s 2>/dev/null || true", shQuote(TimerName(cluster, database))),
		fmt.Sprintf("sudo rm -f %s %s %s",
			shQuote(TimerPath(cluster, database)),
			shQuote(ServicePath(cluster, database)),
			shQuote(ScriptPath(cluster, database))),
		"sudo systemctl daemon-reload",
	}, "\n")
}
