package dbbackup

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRestoreRoundTrip is the ADR-0036 "proven, not assumed" restore proof: it
// stands up a real, throwaway Postgres cluster and round-trips data through the
// exact command shapes the backup + restore renderers emit —
// `pg_dump -Fc | gzip` then `gunzip -c | pg_restore --clean --if-exists -d` —
// asserting the restored rows match the originals. It exercises the load-bearing
// flag/format combo that `bash -n` never could (a wrong -F/--clean/-d would pass
// syntax but fail a real restore). It is hermetic (its own temp PGDATA + unix
// socket, torn down after) and skip-gated: when the Postgres binaries aren't on
// PATH — as in the pure-Go CI image — it t.Skips rather than fails, so CI stays
// green while a developer with Postgres installed gets the real proof. Pure
// os/exec, no cgo.
func TestRestoreRoundTrip(t *testing.T) {
	bins := map[string]string{}
	for _, name := range []string{"initdb", "pg_ctl", "createdb", "psql", "pg_dump", "pg_restore", "gzip", "gunzip"} {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s not on PATH — skipping real-Postgres round-trip proof", name)
		}
		bins[name] = p
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dataDir := filepath.Join(t.TempDir(), "data")
	dumpPath := filepath.Join(t.TempDir(), "app.dump.gz")
	// The unix socket dir must be SHORT (sun_path caps ~104 bytes); t.TempDir()
	// under /var/folders on macOS can overflow it, so use a short /tmp dir.
	sockDir, err := os.MkdirTemp("/tmp", "wnpg")
	if err != nil {
		t.Skipf("cannot create short socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	// PG_* env pointing every client at the socket-only cluster as the postgres
	// superuser (trust auth, no password).
	pgEnv := append(os.Environ(),
		"PGHOST="+sockDir,
		"PGPORT=5432",
		"PGUSER=postgres",
		"PGDATABASE=postgres",
	)
	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, bins[name], args...)
		cmd.Env = pgEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
	}
	psql := func(db, sql string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, bins["psql"], "-d", db, "-tAqc", sql)
		cmd.Env = pgEnv
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("psql -d %s %q: %v\n%s", db, sql, err, out.String())
		}
		return strings.TrimSpace(out.String())
	}

	// initdb a fresh cluster with the postgres superuser + trust auth.
	run("initdb", "-D", dataDir, "-U", "postgres", "-A", "trust", "--no-locale", "-E", "UTF8")

	// Start socket-only (no TCP) so no port is allocated and nothing leaks off-box.
	logPath := filepath.Join(t.TempDir(), "pg.log")
	run("pg_ctl", "-D", dataDir, "-l", logPath, "-w", "-o",
		"-p 5432 -k "+sockDir+" -c listen_addresses=", "start")
	t.Cleanup(func() {
		stop := exec.Command(bins["pg_ctl"], "-D", dataDir, "-m", "immediate", "stop")
		stop.Env = pgEnv
		_ = stop.Run()
	})

	// Seed a source database with a table + rows (owned by a NOLOGIN owner role,
	// as deploy provisions — proving --clean restore preserves ownership).
	run("createdb", "appdb")
	psql("appdb", "CREATE ROLE appowner NOLOGIN")
	psql("appdb", "CREATE TABLE widgets(id int primary key, name text); "+
		"INSERT INTO widgets VALUES (1,'alpha'),(2,'beta'),(3,'gamma'); "+
		"ALTER TABLE widgets OWNER TO appowner")
	if got := psql("appdb", "SELECT count(*) FROM widgets"); got != "3" {
		t.Fatalf("source row count = %q, want 3", got)
	}

	// Backup shape: pg_dump -Fc | gzip → file (the BackupScript pipeline).
	dumpToFile(t, ctx, bins, pgEnv, "appdb", dumpPath)

	// Restore shape: gunzip -c | pg_restore --clean --if-exists -d <fresh db>
	// (the pgRestoreTail pipeline, minus the sudo drop the on-host script adds).
	run("createdb", "restored")
	restoreFromFile(t, ctx, bins, pgEnv, "restored", dumpPath)

	// The restored database must carry the same rows AND the same ownership.
	if got := psql("restored", "SELECT count(*) FROM widgets"); got != "3" {
		t.Fatalf("restored row count = %q, want 3", got)
	}
	if got := psql("restored", "SELECT string_agg(name, ',' ORDER BY id) FROM widgets"); got != "alpha,beta,gamma" {
		t.Fatalf("restored rows = %q, want alpha,beta,gamma", got)
	}
	if got := psql("restored", "SELECT tableowner FROM pg_tables WHERE tablename='widgets'"); got != "appowner" {
		t.Fatalf("restored table owner = %q, want appowner (ownership must survive a data-only restore)", got)
	}

	// Re-restoring must be idempotent (--clean --if-exists), not error on the
	// second pass — the property that makes a retry safe.
	restoreFromFile(t, ctx, bins, pgEnv, "restored", dumpPath)
	if got := psql("restored", "SELECT count(*) FROM widgets"); got != "3" {
		t.Fatalf("re-restore row count = %q, want 3 (idempotent)", got)
	}
}

// dumpToFile runs `pg_dump -Fc <db> | gzip > path` — the BackupScript pipeline.
func dumpToFile(t *testing.T, ctx context.Context, bins map[string]string, env []string, db, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	dump := exec.CommandContext(ctx, bins["pg_dump"], "-Fc", db)
	dump.Env = env
	gz := exec.CommandContext(ctx, bins["gzip"])
	gz.Env = env
	pipe, err := dump.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	gz.Stdin = pipe
	gz.Stdout = f
	var dumpErr, gzErr bytes.Buffer
	dump.Stderr = &dumpErr
	gz.Stderr = &gzErr
	if err := gz.Start(); err != nil {
		t.Fatal(err)
	}
	if err := dump.Run(); err != nil {
		t.Fatalf("pg_dump: %v\n%s", err, dumpErr.String())
	}
	if err := gz.Wait(); err != nil {
		t.Fatalf("gzip: %v\n%s", err, gzErr.String())
	}
}

// restoreFromFile runs `gunzip -c <path> | pg_restore --clean --if-exists -d <db>`
// — the pgRestoreTail pipeline. Errors from pg_restore are fatal, but its stderr
// notices ("does not exist, skipping" on a first --clean) are tolerated: the
// renderer deliberately omits --exit-on-error for exactly this reason.
func restoreFromFile(t *testing.T, ctx context.Context, bins map[string]string, env []string, db, path string) {
	t.Helper()
	gunzip := exec.CommandContext(ctx, bins["gunzip"], "-c", path)
	gunzip.Env = env
	restore := exec.CommandContext(ctx, bins["pg_restore"], "--clean", "--if-exists", "-d", db)
	restore.Env = env
	pipe, err := gunzip.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	restore.Stdin = pipe
	var gunzipErr, restoreOut bytes.Buffer
	gunzip.Stderr = &gunzipErr
	restore.Stdout = &restoreOut
	restore.Stderr = &restoreOut
	if err := restore.Start(); err != nil {
		t.Fatal(err)
	}
	if err := gunzip.Run(); err != nil {
		t.Fatalf("gunzip: %v\n%s", err, gunzipErr.String())
	}
	if err := restore.Wait(); err != nil {
		t.Fatalf("pg_restore: %v\n%s", err, restoreOut.String())
	}
}
