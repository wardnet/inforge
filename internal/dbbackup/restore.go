package dbbackup

import (
	"fmt"
	"strings"
)

// RestoreConfig is the input to the on-host restore renderers for one logical
// database on its cluster host.
type RestoreConfig struct {
	Port     int    // the cluster's TCP port (postgres.ClusterPort)
	Database string // the logical database name (pg_restore target)
}

func (c RestoreConfig) validate() error {
	switch {
	case c.Port <= 0:
		return fmt.Errorf("dbbackup restore: invalid port %d", c.Port)
	case c.Database == "":
		return fmt.Errorf("dbbackup restore: empty database name")
	}
	return nil
}

// RemoteRestorePath is the on-host path `inforge db restore --from-dump` streams a
// local dump to before restoring from it. It lives under ConfDir (created by the
// stream write even on a host with no backups configured) and is namespaced by
// cluster+database so concurrent restores of different databases never collide.
func RemoteRestorePath(cluster, database string) string {
	return fmt.Sprintf("%s/restore-%s-%s.dump.gz", ConfDir, cluster, database)
}

// pgRestoreTail is the shared restore invocation both source paths pipe a gzipped
// custom-format dump into. Restore is DATA-ONLY (roles are re-minted by
// `inforge deploy`, never captured in a single-database pg_dump): --clean
// --if-exists makes a re-restore idempotent by dropping existing objects first,
// and ownership/ACLs are preserved (the owner role exists — deploy minted it), so
// restored objects land owned by the real owner. It runs as the postgres OS user
// over local peer auth (the cluster is private-only). The caller runs the whole
// script as root, so `sudo -u postgres` is a root→postgres drop.
func pgRestoreTail(c RestoreConfig) string {
	return fmt.Sprintf("sudo -u postgres pg_restore -p %d -w --clean --if-exists -d %s", c.Port, shQuote(c.Database))
}

// RestoreFromKeyScript renders the bash `inforge db restore` SSH-triggers to
// restore an R2 backup key on the cluster host. It sources the backup-scoped R2
// credential already delivered to the host (CredentialPath, slice #4), streams
// the object down, gunzips it, and pipes into pg_restore. The whole script is run
// as root by the caller so it can read the root-owned credential. It fails with a
// clear message when the credential is absent — restore-from-key requires backups
// to be provisioned on the host; the --from-dump path (RestoreFromFileScript)
// needs no R2 access and works on a fresh cluster.
func RestoreFromKeyScript(c RestoreConfig, bucket, endpoint, key string) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	switch {
	case bucket == "":
		return "", fmt.Errorf("dbbackup restore: empty bucket")
	case endpoint == "":
		return "", fmt.Errorf("dbbackup restore: empty endpoint")
	case key == "":
		return "", fmt.Errorf("dbbackup restore: empty key")
	}
	return strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"cred=" + shQuote(CredentialPath),
		`if [ ! -r "$cred" ]; then`,
		`  echo "wardnet-restore: no R2 backup credential at $cred — restore-from-key needs backups provisioned on this host (deploy a backup-enabled database first, or use --from-dump)" >&2`,
		"  exit 1",
		"fi",
		// The credential file is the AWS_* chain the on-host `aws` reads; source it
		// and set the same non-secret R2 env the backup unit sets.
		`set -a; . "$cred"; set +a`,
		"export AWS_DEFAULT_REGION=auto",
		"export AWS_EC2_METADATA_DISABLED=true",
		"bucket=" + shQuote(bucket),
		"endpoint=" + shQuote(endpoint),
		"key=" + shQuote(key),
		fmt.Sprintf(`aws --endpoint-url "$endpoint" s3 cp "s3://${bucket}/${key}" - | gunzip -c | %s`, pgRestoreTail(c)),
		"",
	}, "\n"), nil
}

// RestoreFromFileScript renders the bash that restores a gzipped custom-format
// dump already streamed to remotePath on the host (the --from-dump path: no R2,
// works on a host with no backups configured). The whole script runs as root; the
// streamed file is root-owned 0600. It removes the file on exit (success or
// failure) so a manual migration dump never lingers on the host.
func RestoreFromFileScript(c RestoreConfig, remotePath string) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	if remotePath == "" {
		return "", fmt.Errorf("dbbackup restore: empty remote dump path")
	}
	return strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		fmt.Sprintf("trap 'rm -f %s' EXIT", shQuote(remotePath)),
		fmt.Sprintf("gunzip -c %s | %s", shQuote(remotePath), pgRestoreTail(c)),
		"",
	}, "\n"), nil
}
