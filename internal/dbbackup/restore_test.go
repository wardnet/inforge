package dbbackup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreFromKeyScript(t *testing.T) {
	cfg := RestoreConfig{Port: 5432, Database: "appdb"}
	got, err := RestoreFromKeyScript(cfg, "wardnet-backups", "https://acct.r2.cloudflarestorage.com", "prd/use1/pg/appdb/20260101T000000Z.dump.gz")
	if err != nil {
		t.Fatalf("RestoreFromKeyScript: %v", err)
	}
	for _, want := range []string{
		"set -euo pipefail",
		CredentialPath,
		`. "$cred"`,
		`s3 cp "s3://${bucket}/${key}" -`,
		"gunzip -c",
		"sudo -u postgres pg_restore -p 5432 -w --clean --if-exists -d 'appdb'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q\n---\n%s", want, got)
		}
	}
	// restore-from-key must guard on the credential being present.
	if !strings.Contains(got, "restore-from-key needs backups provisioned") {
		t.Errorf("missing credential-absent guard\n---\n%s", got)
	}
}

func TestRestoreFromFileScript(t *testing.T) {
	cfg := RestoreConfig{Port: 5433, Database: "appdb"}
	remote := RemoteRestorePath("pg", "appdb")
	got, err := RestoreFromFileScript(cfg, remote)
	if err != nil {
		t.Fatalf("RestoreFromFileScript: %v", err)
	}
	for _, want := range []string{
		"set -euo pipefail",
		"trap 'rm -f " + shQuote(remote) + "' EXIT",
		"gunzip -c " + shQuote(remote),
		"pg_restore -p 5433 -w --clean --if-exists -d 'appdb'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q\n---\n%s", want, got)
		}
	}
	// --from-dump path must not reference R2 (no aws, no credential).
	if strings.Contains(got, "aws ") || strings.Contains(got, CredentialPath) {
		t.Errorf("--from-dump script must not touch R2\n---\n%s", got)
	}
}

func TestRestoreValidation(t *testing.T) {
	if _, err := RestoreFromKeyScript(RestoreConfig{Port: 0, Database: "d"}, "b", "e", "k"); err == nil {
		t.Error("want error for invalid port")
	}
	if _, err := RestoreFromKeyScript(RestoreConfig{Port: 5432, Database: ""}, "b", "e", "k"); err == nil {
		t.Error("want error for empty database")
	}
	if _, err := RestoreFromKeyScript(RestoreConfig{Port: 5432, Database: "d"}, "", "e", "k"); err == nil {
		t.Error("want error for empty bucket")
	}
	if _, err := RestoreFromFileScript(RestoreConfig{Port: 5432, Database: "d"}, ""); err == nil {
		t.Error("want error for empty remote path")
	}
}

func TestRestoreScriptsParse(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cfg := RestoreConfig{Port: 5432, Database: "appdb"}
	key, err := RestoreFromKeyScript(cfg, "b", "https://e", "prd/use1/pg/appdb/20260101T000000Z.dump.gz")
	if err != nil {
		t.Fatalf("RestoreFromKeyScript: %v", err)
	}
	file, err := RestoreFromFileScript(cfg, RemoteRestorePath("pg", "appdb"))
	if err != nil {
		t.Fatalf("RestoreFromFileScript: %v", err)
	}
	for name, script := range map[string]string{"restore_key.sh": key, "restore_file.sh": file} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(p, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(bash, "-n", p).CombinedOutput(); err != nil {
				t.Fatalf("bash -n failed: %v\n%s\n---\n%s", err, out, script)
			}
		})
	}
}
