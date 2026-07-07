// Package dbbackup renders the on-host per-database backup delivery for a
// self-hosted Postgres cluster (ADR-0036): the idempotent awscli install, the
// `pg_dump -Fc | gzip` → R2 upload script with retention prune, and the systemd
// .service + .timer that drives it on a cadence. Like internal/postgres,
// internal/otelcol and internal/nginx it is pure — deploy-side only, with no
// Pulumi/provider dependency; the program wires it to cluster hosts. Backups run
// on the host because a self-hosted cluster is private-only (unreachable from the
// deploy machine), and upload with a backup-scoped R2 credential delivered under
// the reserved secret namespace.
package dbbackup

import (
	"fmt"
	"strings"
)

const (
	// ConfDir holds the R2 credential EnvironmentFile and the per-database backup
	// scripts inforge writes.
	ConfDir = "/etc/wardnet/backups"

	// CredentialPath is the systemd EnvironmentFile (0600, root) carrying the
	// backup-scoped R2 credential as AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY —
	// the exact chain `aws` reads, so the on-host script needs no delimiter parsing.
	CredentialPath = ConfDir + "/r2.env"
)

// AuthSecretNamespace / the two key names locate the backup-scoped R2 credential in
// the env's secrets.enc.yaml. It is an inforge RESERVED secret (like the
// observability OTLP credential, #169) — it lives under the store's `reserved:`
// namespace, referenced by no service and read directly by the deploy, so a user
// service may use the container name "backups" without colliding. It is stored as
// TWO keys (not one colon-joined value) mirroring the AWS_* chain, so a secret
// containing a `:` is never mis-split.
const (
	AuthSecretNamespace = "backups"
	AccessKeyIDKey      = "r2_access_key_id"
	SecretAccessKeyKey  = "r2_secret_access_key"
)

// unitName is the systemd unit basename (no suffix) for one logical database's
// backup on its cluster. Cluster+database uniquely identify it on the host.
func unitName(cluster, database string) string {
	return fmt.Sprintf("wardnet-backup-%s-%s", cluster, database)
}

// ServiceName / TimerName are the systemd unit names (with suffix) for a database's
// backup oneshot and its driving timer.
func ServiceName(cluster, database string) string { return unitName(cluster, database) + ".service" }
func TimerName(cluster, database string) string   { return unitName(cluster, database) + ".timer" }

// ServicePath / TimerPath / ScriptPath are the on-host file paths for a database's
// backup oneshot unit, its timer, and the backup shell the oneshot executes.
func ServicePath(cluster, database string) string {
	return "/etc/systemd/system/" + ServiceName(cluster, database)
}
func TimerPath(cluster, database string) string {
	return "/etc/systemd/system/" + TimerName(cluster, database)
}
func ScriptPath(cluster, database string) string {
	return fmt.Sprintf("%s/%s-%s.sh", ConfDir, cluster, database)
}

// ObjectPrefix is the R2 key prefix for one (env, region, cluster, database)'s
// backups. Both env and region are in the key so one bucket serves every environment
// AND every region: a regional database-cluster deploys under the same name to every
// region, so without the region segment each region's retention prune would see and
// delete the others' archives (region is the scope slug, or "global" for the global
// slice). Objects are named `<prefix><timestamp>.dump.gz`; the ISO-8601 UTC timestamp
// sorts lexically in chronological order, which the retention prune relies on.
func ObjectPrefix(env, region, cluster, database string) string {
	return fmt.Sprintf("%s/%s/%s/%s/", env, region, cluster, database)
}

// ParseObjectKey is the inverse of ObjectPrefix + the "<timestamp>.dump.gz" object
// name: it decomposes a backup key `<env>/<region>/<cluster>/<database>/<object>`
// into its segments. Keeping the parse next to the format makes this package the
// single source of the key layout — a change to ObjectPrefix that shifted a segment
// would break ParseObjectKey's tests here rather than silently desyncing a
// hand-rolled splitter in a consumer. Resource names never contain "/", so a valid
// key has exactly five non-empty slash-separated segments.
func ParseObjectKey(key string) (env, region, cluster, database, object string, err error) {
	parts := strings.Split(key, "/")
	if len(parts) != 5 {
		return "", "", "", "", "", fmt.Errorf("dbbackup: %q is not a valid backup key (<env>/<region>/<cluster>/<database>/<timestamp>.dump.gz)", key)
	}
	for _, p := range parts {
		if p == "" {
			return "", "", "", "", "", fmt.Errorf("dbbackup: %q has an empty segment (<env>/<region>/<cluster>/<database>/<timestamp>.dump.gz)", key)
		}
	}
	return parts[0], parts[1], parts[2], parts[3], parts[4], nil
}
