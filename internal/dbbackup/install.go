package dbbackup

import "strings"

// InstallScript renders the idempotent shell that installs the AWS CLI on a cluster
// host, used by the backup timer to upload dumps to R2 (S3-compatible). It is a
// no-op when `aws` is already on PATH, so it only runs apt on the first deploy;
// serialized after the Postgres apt install by the caller so co-located apt runs
// never race the dpkg lock.
func InstallScript() string {
	return strings.Join([]string{
		"set -e",
		"if ! command -v aws >/dev/null 2>&1; then",
		"  sudo apt-get update",
		"  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y awscli",
		"fi",
	}, "\n")
}

// CredentialScript writes the backup-scoped R2 credential as a systemd
// EnvironmentFile (0600, root) at CredentialPath, exposing the exact AWS_* chain the
// on-host `aws` reads. Both values are supplied decrypted by the caller, which wraps
// the whole command as a Pulumi secret so it is encrypted in state and never appears
// as plaintext. Least-privilege: the DB host can write backups and nothing else.
func CredentialScript(accessKeyID, secretAccessKey string) string {
	env := strings.Join([]string{
		"AWS_ACCESS_KEY_ID=" + accessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + secretAccessKey,
		"",
	}, "\n")
	return "set -e\n" + writeFileScript(CredentialPath, env, "0600", "root:root")
}
