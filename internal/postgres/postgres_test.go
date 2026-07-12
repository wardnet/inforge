package postgres

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPaths(t *testing.T) {
	if got := MountDir("edge"); got != "/var/lib/wardnet/db/edge" {
		t.Errorf("MountDir = %q", got)
	}
	if got := DataDir("edge"); got != "/var/lib/wardnet/db/edge/pgdata" {
		t.Errorf("DataDir = %q", got)
	}
	if got := ServiceName("edge"); got != "wardnet-db-edge.service" {
		t.Errorf("ServiceName = %q", got)
	}
	if got := UnitPath("edge"); got != "/etc/systemd/system/wardnet-db-edge.service" {
		t.Errorf("UnitPath = %q", got)
	}
	if BinDir("17") != "/usr/lib/postgresql/17/bin" {
		t.Errorf("BinDir(17) = %q", BinDir("17"))
	}
	if BinDir("") != "/usr/lib/postgresql/"+DefaultVersion+"/bin" {
		t.Errorf("BinDir default = %q", BinDir(""))
	}
}

func TestClusterPortAndRange(t *testing.T) {
	if ClusterPort(0) != 5432 || ClusterPort(3) != 5435 {
		t.Errorf("ClusterPort mapping wrong: %d %d", ClusterPort(0), ClusterPort(3))
	}
	if !InReservedPortRange(5432) || !InReservedPortRange(5432+MaxClustersPerHost-1) {
		t.Error("in-range ports must report true")
	}
	if InReservedPortRange(5431) || InReservedPortRange(5432+MaxClustersPerHost) {
		t.Error("out-of-range ports must report false")
	}
}

func validCfg() ClusterConfig {
	return ClusterConfig{Cluster: "edge", Version: "17", ListenIP: "10.0.0.5", Port: 5432, NetworkCIDR: "10.0.0.0/16"}
}

func TestRenderPostgresqlConf(t *testing.T) {
	got, err := RenderPostgresqlConf(validCfg())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"listen_addresses = 'localhost,10.0.0.5'",
		"port = 5432",
		"unix_socket_directories = '/var/run/postgresql'",
		"data_directory = '/var/lib/wardnet/db/edge/pgdata'",
		"password_encryption = scram-sha-256",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("postgresql.conf missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "0.0.0.0") {
		t.Error("postgresql.conf must not listen on 0.0.0.0")
	}
}

func TestRenderHBA(t *testing.T) {
	got, err := RenderHBA(validCfg())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "local   all       all                      peer") {
		t.Error("HBA missing local peer rule")
	}
	if !strings.Contains(got, "host    all       all   10.0.0.0/16   scram-sha-256") {
		t.Errorf("HBA missing private-CIDR scram rule:\n%s", got)
	}
	if strings.Contains(got, "0.0.0.0/0") {
		t.Error("HBA must never admit 0.0.0.0/0 — cluster is private-only")
	}
}

func TestConfigValidation(t *testing.T) {
	for name, mut := range map[string]func(*ClusterConfig){
		"cluster": func(c *ClusterConfig) { c.Cluster = "" },
		"listen":  func(c *ClusterConfig) { c.ListenIP = "" },
		"port":    func(c *ClusterConfig) { c.Port = 0 },
		"cidr":    func(c *ClusterConfig) { c.NetworkCIDR = "" },
	} {
		c := validCfg()
		mut(&c)
		if _, err := RenderPostgresqlConf(c); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestInstallScript(t *testing.T) {
	s := InstallScript("17")
	for _, want := range []string{
		"create_main_cluster = false",
		"/etc/apt/sources.list.d/pgdg.list",
		"apt.postgresql.org",
		"postgresql-17",
		"install ok installed", // idempotent guard
		// Regression (v5 deploy): the signing key is installed ARMORED (.asc) and
		// referenced via signed-by — no `gpg --dearmor` (fails with no controlling
		// tty). Guarded on the keyring file; downloaded to a temp then installed.
		"if [ ! -f /usr/share/keyrings/pgdg.asc ]",
		`sudo install -m 0644 "$asc" /usr/share/keyrings/pgdg.asc`,
		"signed-by=/usr/share/keyrings/pgdg.asc",
		"DPkg::Lock::Timeout=300",
		// The index refresh retries a held lists lock (aptlock.UpdateCmd).
		"apt-get update blocked",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("InstallScript missing %q", want)
		}
	}
	// No gpg at all — the armored key is used directly.
	if strings.Contains(s, "gpg --dearmor") {
		t.Error("must not use gpg --dearmor (fails with no controlling tty; apt reads the .asc directly)")
	}
	// `apt-get update` must live in the install block, NOT be gated on pgdg.list —
	// so a partial earlier run (pgdg.list written, update skipped) still refreshes.
	if strings.Contains(s, "if [ ! -f /etc/apt/sources.list.d/pgdg.list ]") {
		t.Error("apt-get update must not be gated on the pgdg.list repo file")
	}
	if !strings.Contains(InstallScript(""), "postgresql-"+DefaultVersion) {
		t.Error("empty version must default")
	}
}

// TestApplyScriptDoesNotChmodPGDATA guards the fix for the v5 deploy failure where
// writing postgresql.conf into PGDATA ran `install -d -m 0755 <PGDATA>`, flipping
// the data dir off 0700 so the postmaster refused to start. The parent dir must be
// ensured with `mkdir -p` (which never re-modes an existing dir).
func TestApplyScriptDoesNotChmodPGDATA(t *testing.T) {
	s, err := ApplyScript(validCfg())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "sudo mkdir -p") {
		t.Errorf("ApplyScript must ensure the parent dir with mkdir -p\n%s", s)
	}
	if strings.Contains(s, "install -d -m 0755") {
		t.Errorf("ApplyScript must not `install -d -m 0755` a config parent — it re-modes PGDATA to 0755\n%s", s)
	}
}

// TestInstallAndApplyParse runs the rendered install/apply/mount/init shells
// through `bash -n`.
func TestInstallAndApplyParse(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	apply, err := ApplyScript(validCfg())
	if err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"install.sh": InstallScript("17"),
		"apply.sh":   apply,
		"mount.sh":   MountScript("/dev/sdb", "core"),
		"init.sh":    InitClusterScript("core", "17"),
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(p, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(bash, "-n", p).CombinedOutput(); err != nil {
				t.Fatalf("bash -n failed: %v\n%s", err, out)
			}
		})
	}
}

func TestMountScript(t *testing.T) {
	s := MountScript("/dev/sdb", "edge")
	for _, want := range []string{
		"dev='/dev/sdb'",
		"mnt='/var/lib/wardnet/db/edge'",
		"mkfs.ext4 -F",
		"blkid -s UUID -o value",
		"/etc/fstab",
		"mountpoint -q",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("MountScript missing %q", want)
		}
	}
	// The mkfs must be guarded so existing data is never reformatted.
	if !strings.Contains(s, `if ! sudo blkid "$dev"`) {
		t.Error("MountScript must guard mkfs behind a blkid check")
	}
}

func TestInitClusterScript(t *testing.T) {
	s := InitClusterScript("edge", "17")
	for _, want := range []string{
		`if [ ! -f "$data/PG_VERSION" ]; then`, // no re-init of a populated volume
		"/usr/lib/postgresql/17/bin/initdb",
		"--auth-local=peer",
		"--auth-host=scram-sha-256",
		"-o postgres -g postgres -m 0700",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("InitClusterScript missing %q", want)
		}
	}
}

func TestUnitFile(t *testing.T) {
	u := UnitFile("edge", "17", 5433)
	for _, want := range []string{
		"Type=notify",
		"User=postgres",
		"RequiresMountsFor=/var/lib/wardnet/db/edge",
		"ExecStart=/usr/lib/postgresql/17/bin/postgres -D /var/lib/wardnet/db/edge/pgdata -p 5433",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("UnitFile missing %q", want)
		}
	}
}

func TestApplyScript(t *testing.T) {
	s, err := ApplyScript(validCfg())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/var/lib/wardnet/db/edge/pgdata/postgresql.conf",
		"/var/lib/wardnet/db/edge/pgdata/pg_hba.conf",
		"/etc/systemd/system/wardnet-db-edge.service",
		"systemctl daemon-reload",
		"systemctl restart 'wardnet-db-edge.service'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("ApplyScript missing %q", want)
		}
	}
}

func TestEnsureOwnerScript(t *testing.T) {
	s := EnsureOwnerScript(5432, "app")
	if !strings.Contains(s, "sudo -u postgres psql -p 5432 -w -v ON_ERROR_STOP=1") {
		t.Errorf("owner script transport wrong:\n%s", s)
	}
	if !strings.Contains(s, `CREATE ROLE "app" NOLOGIN`) {
		t.Error("owner role must be NOLOGIN")
	}
	if !strings.Contains(s, "WHERE rolname = 'app'") {
		t.Error("owner create must be guarded (idempotent)")
	}
}

func TestEnsureDatabaseScript(t *testing.T) {
	s := EnsureDatabaseScript(5432, "tunneller", "app")
	if !strings.Contains(s, "SELECT 1 FROM pg_database WHERE datname='tunneller';") {
		t.Errorf("db creation must be guarded by an existence check:\n%s", s)
	}
	if !strings.Contains(s, `)" != "1" ]; then`) {
		t.Error("db guard must compare psql output to 1")
	}
	if !strings.Contains(s, "createdb -p 5432 -O 'app' 'tunneller'") {
		t.Errorf("createdb invocation wrong:\n%s", s)
	}
}

func TestMintRoleScript(t *testing.T) {
	s, err := MintRoleScript(5433, "svc", "s3cr3t", "tunneller", "tunneller-owner", "rw")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"psql -p 5433 -w -v ON_ERROR_STOP=1 -d 'tunneller'",
		`CREATE ROLE "svc" LOGIN PASSWORD 's3cr3t'`,
		`GRANT USAGE, CREATE ON SCHEMA public TO "svc"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "svc" IN SCHEMA public GRANT SELECT ON TABLES TO "tunneller_ro"`,
		"<<'INFORGE_PGSQL'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("MintRoleScript missing %q in:\n%s", want, s)
		}
	}
	if _, err := MintRoleScript(5432, "svc", "pw", "db", "db-owner", "bogus"); err == nil {
		t.Error("unknown permission must error")
	}
}

// REASSIGN OWNED is per-database: the downgrade must run connected to the granted
// database, not the cluster default.
func TestMintRoleScriptROReassignsInTargetDatabase(t *testing.T) {
	s, err := MintRoleScript(5433, "svc", "s3cr3t", "tunneller", "tunneller-owner", "ro")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "psql -p 5433 -w -v ON_ERROR_STOP=1 -d 'tunneller'") {
		t.Errorf("ro mint must connect to the granted database:\n%s", s)
	}
	if !strings.Contains(s, `REASSIGN OWNED BY "svc" TO "tunneller-owner"`) {
		t.Errorf("ro mint must reassign owned objects to the database owner:\n%s", s)
	}
}

func TestDropRoleScript(t *testing.T) {
	s := DropRoleScript(5432, "svc", "app")
	for _, want := range []string{
		`REASSIGN OWNED BY "svc" TO "app"`,
		`DROP OWNED BY "svc"`,
		`DROP ROLE IF EXISTS "svc"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("DropRoleScript missing %q", want)
		}
	}
}

func TestMintMonitorRoleScript(t *testing.T) {
	s := MintMonitorRoleScript(5433, "wardnet-prd-use1-dbrole-pg-regional-otelmon", "pw", []string{"tenants"})
	for _, want := range []string{
		"sudo -u postgres psql -p 5433",
		"ON_ERROR_STOP=1",
		`GRANT pg_monitor TO "wardnet-prd-use1-dbrole-pg-regional-otelmon"`,
		`GRANT CONNECT ON DATABASE "tenants"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("MintMonitorRoleScript missing %q\n%s", want, s)
		}
	}
}
