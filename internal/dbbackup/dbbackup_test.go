package dbbackup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseObjectKeyRoundTrip(t *testing.T) {
	// ParseObjectKey must invert ObjectPrefix (+ the object name) so the CLI never
	// hand-splits the key format.
	prefix := ObjectPrefix("prd", "use1", "core", "ddns")
	key := prefix + "20260101T000000Z.dump.gz"
	env, region, cluster, database, object, err := ParseObjectKey(key)
	if err != nil {
		t.Fatalf("ParseObjectKey(%q): %v", key, err)
	}
	if env != "prd" || region != "use1" || cluster != "core" || database != "ddns" || object != "20260101T000000Z.dump.gz" {
		t.Fatalf("got (%q,%q,%q,%q,%q)", env, region, cluster, database, object)
	}
}

func TestParseObjectKeyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{
		"",
		"prd/use1/core/ddns",                  // 4 segments (missing object)
		"prd/use1/core/ddns/ts/extra.dump.gz", // 6 segments
		"prd//core/ddns/ts.dump.gz",           // empty region segment
	} {
		if _, _, _, _, _, err := ParseObjectKey(bad); err == nil {
			t.Errorf("ParseObjectKey(%q) = nil error, want error", bad)
		}
	}
}

func validConfig() BackupConfig {
	return BackupConfig{
		Env:         "prd",
		Region:      "use1",
		Cluster:     "core",
		Database:    "ddns",
		Port:        5432,
		ClusterUnit: "wardnet-db-core.service",
		Bucket:      "wardnet-backups",
		Endpoint:    "https://acct.r2.cloudflarestorage.com",
		Keep:        7,
		Interval:    24 * time.Hour,
	}
}

func TestObjectPrefix(t *testing.T) {
	// The region segment keeps a regional cluster's per-region backups (and prune) apart.
	if got, want := ObjectPrefix("prd", "use1", "core", "ddns"), "prd/use1/core/ddns/"; got != want {
		t.Fatalf("ObjectPrefix = %q, want %q", got, want)
	}
	if got, want := ObjectPrefix("prd", "global", "core", "ddns"), "prd/global/core/ddns/"; got != want {
		t.Fatalf("ObjectPrefix(global) = %q, want %q", got, want)
	}
}

func TestUnitNames(t *testing.T) {
	if got, want := ServiceName("core", "ddns"), "wardnet-backup-core-ddns.service"; got != want {
		t.Fatalf("ServiceName = %q, want %q", got, want)
	}
	if got, want := TimerName("core", "ddns"), "wardnet-backup-core-ddns.timer"; got != want {
		t.Fatalf("TimerName = %q, want %q", got, want)
	}
	if got, want := ScriptPath("core", "ddns"), "/etc/wardnet/backups/core-ddns.sh"; got != want {
		t.Fatalf("ScriptPath = %q, want %q", got, want)
	}
}

func TestBackupScript(t *testing.T) {
	got, err := BackupScript(validConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The object key stamps env/region/cluster/database and a UTC timestamp.
	for _, want := range []string{
		"set -euo pipefail",
		`prefix='prd/use1/core/ddns/'`,
		"ts=$(date -u +%Y%m%dT%H%M%SZ)",
		`sudo -u postgres pg_dump -p 5432 -w -Fc 'ddns' | gzip | aws --endpoint-url "$endpoint" s3 cp - "s3://${bucket}/${key}"`,
		// Retention prune keeps the newest 7 (deletes n-7 oldest) in one batched call,
		// and is best-effort so it never fails an already-uploaded backup.
		"del=$(( n - 7 ))",
		`aws --endpoint-url "$endpoint" s3api delete-objects --bucket "$bucket" --delete "$payload"`,
		`prune || echo "wardnet-backup: retention prune failed (backup already uploaded)" >&2`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("BackupScript missing %q\n---\n%s", want, got)
		}
	}
	// The credential must never appear inline — it is read from the EnvironmentFile.
	if strings.Contains(got, "AWS_SECRET_ACCESS_KEY=") {
		t.Errorf("BackupScript must not inline the credential:\n%s", got)
	}
}

func TestBackupScriptValidation(t *testing.T) {
	cases := map[string]func(*BackupConfig){
		"empty env":      func(c *BackupConfig) { c.Env = "" },
		"empty region":   func(c *BackupConfig) { c.Region = "" },
		"empty cluster":  func(c *BackupConfig) { c.Cluster = "" },
		"empty database": func(c *BackupConfig) { c.Database = "" },
		"bad port":       func(c *BackupConfig) { c.Port = 0 },
		"empty bucket":   func(c *BackupConfig) { c.Bucket = "" },
		"empty endpoint": func(c *BackupConfig) { c.Endpoint = "" },
		"keep zero":      func(c *BackupConfig) { c.Keep = 0 },
		"interval zero":  func(c *BackupConfig) { c.Interval = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(&c)
			if _, err := BackupScript(c); err == nil {
				t.Fatalf("expected validation error for %s", name)
			}
		})
	}
}

func TestUnitFile(t *testing.T) {
	got := UnitFile(validConfig())
	for _, want := range []string{
		"Type=oneshot",
		"EnvironmentFile=" + CredentialPath,
		"Environment=AWS_DEFAULT_REGION=auto",
		"After=network-online.target wardnet-db-core.service",
		"ExecStart=/etc/wardnet/backups/core-ddns.sh",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("UnitFile missing %q\n---\n%s", want, got)
		}
	}
}

func TestTimerFile(t *testing.T) {
	c := validConfig()
	got := TimerFile(c)
	// 24h == 86400s cadence; the first fire is prompt (OnBootSec capped at 300s) so a
	// fresh database is not left unbacked-up for a full interval.
	for _, want := range []string{"OnBootSec=300s", "OnUnitActiveSec=86400s", "WantedBy=timers.target"} {
		if !strings.Contains(got, want) {
			t.Errorf("TimerFile missing %q\n---\n%s", want, got)
		}
	}
	// A sub-minute interval is floored to 60s (systemd granularity); OnBootSec follows.
	c.Interval = 5 * time.Second
	got = TimerFile(c)
	for _, want := range []string{"OnBootSec=60s", "OnUnitActiveSec=60s"} {
		if !strings.Contains(got, want) {
			t.Errorf("TimerFile should floor to 60s, missing %q\n%s", want, got)
		}
	}
}

func TestApplyScriptWritesUnitsAndEnablesTimer(t *testing.T) {
	got, err := ApplyScript(validConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"/etc/wardnet/backups/core-ddns.sh",
		TimerPath("core", "ddns"),
		ServicePath("core", "ddns"),
		"sudo systemctl daemon-reload",
		"sudo systemctl enable --now 'wardnet-backup-core-ddns.timer'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ApplyScript missing %q\n---\n%s", want, got)
		}
	}
}

func TestRemoveScript(t *testing.T) {
	got := RemoveScript("core", "ddns")
	for _, want := range []string{
		"sudo systemctl disable --now 'wardnet-backup-core-ddns.timer'",
		"sudo rm -f",
		"sudo systemctl daemon-reload",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RemoveScript missing %q\n---\n%s", want, got)
		}
	}
}

func TestCredentialScript(t *testing.T) {
	got := CredentialScript("AKID", "SECRET")
	// The credential is base64-transported, never in argv as plaintext.
	if strings.Contains(got, "AKID") || strings.Contains(got, "SECRET") {
		t.Errorf("CredentialScript leaks plaintext credential into argv:\n%s", got)
	}
	if !strings.Contains(got, CredentialPath) {
		t.Errorf("CredentialScript missing target path\n%s", got)
	}
	if !strings.Contains(got, "-m '0600'") {
		t.Errorf("CredentialScript must write 0600\n%s", got)
	}
}

// TestGeneratedShellParses runs the rendered scripts through `bash -n` so a syntax
// error in the generated shell is caught at test time rather than on a host.
func TestGeneratedShellParses(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	backup, err := BackupScript(validConfig())
	if err != nil {
		t.Fatalf("BackupScript: %v", err)
	}
	apply, err := ApplyScript(validConfig())
	if err != nil {
		t.Fatalf("ApplyScript: %v", err)
	}
	scripts := map[string]string{
		"backup.sh":  backup,
		"apply.sh":   apply,
		"remove.sh":  "set -e\n" + RemoveScript("core", "ddns"),
		"install.sh": InstallScript(),
		"cred.sh":    CredentialScript("AKID", "SECRET"),
	}
	for name, script := range scripts {
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

func TestInstallScriptIdempotent(t *testing.T) {
	got := InstallScript()
	for _, want := range []string{"command -v aws", "apt-get -o DPkg::Lock::Timeout=300 install -y awscli"} {
		if !strings.Contains(got, want) {
			t.Errorf("InstallScript missing %q\n---\n%s", want, got)
		}
	}
}
