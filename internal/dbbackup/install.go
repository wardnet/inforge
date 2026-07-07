package dbbackup

import (
	"strings"

	"github.com/wardnet/inforge/internal/aptlock"
)

// InstallScript renders the idempotent shell that installs the AWS CLI on a cluster
// host, used by the backup timer to upload dumps to R2 (S3-compatible). It is a
// no-op when `aws` is already on PATH.
//
// It installs the official AWS CLI **v2** bundle, NOT the `awscli` apt package: that
// package was dropped from Ubuntu 24.04 (noble) — `apt-get install awscli` fails
// "Package 'awscli' has no installation candidate" — and v2 is the current,
// supported release. The bundle is a self-contained installer (extracted with
// unzip, the only apt dependency); `aws/install --update` is idempotent.
func InstallScript() string {
	return strings.Join([]string{
		"set -e",
		"if ! command -v aws >/dev/null 2>&1; then",
		"  if ! command -v unzip >/dev/null 2>&1; then",
		// Refresh the index with a retry (a sibling install or the apt-daily timer may
		// hold the lists lock — DPkg::Lock::Timeout does not cover `apt-get update`).
		"    " + aptlock.UpdateCmd(),
		"    sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y unzip",
		"  fi",
		"  _awsd=$(mktemp -d)",
		`  curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-$(uname -m).zip" -o "$_awsd/awscliv2.zip"`,
		`  unzip -q "$_awsd/awscliv2.zip" -d "$_awsd"`,
		`  sudo "$_awsd/aws/install" --update`,
		`  rm -rf "$_awsd"`,
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
