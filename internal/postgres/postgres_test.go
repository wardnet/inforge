package postgres

import (
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
	} {
		if !strings.Contains(s, want) {
			t.Errorf("InstallScript missing %q", want)
		}
	}
	if !strings.Contains(InstallScript(""), "postgresql-"+DefaultVersion) {
		t.Error("empty version must default")
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
	s, err := MintRoleScript(5433, "svc", "s3cr3t", "tunneller", "rw")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"psql -p 5433 -w -v ON_ERROR_STOP=1 -d 'tunneller'",
		`CREATE ROLE "svc" LOGIN PASSWORD 's3cr3t'`,
		`GRANT USAGE, CREATE ON SCHEMA public TO "svc"`,
		"<<'INFORGE_PGSQL'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("MintRoleScript missing %q in:\n%s", want, s)
		}
	}
	if _, err := MintRoleScript(5432, "svc", "pw", "db", "bogus"); err == nil {
		t.Error("unknown permission must error")
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
