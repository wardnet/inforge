package postgres

import (
	"fmt"
	"strings"

	"github.com/wardnet/inforge/internal/pgrole"
)

// psqlDelimiter is the heredoc marker for piping SQL into psql. It is single-quoted
// in the rendered shell so the body is passed verbatim — critical because role SQL
// contains `$$` (DO blocks) and generated-password literals the shell must not touch.
const psqlDelimiter = "INFORGE_PGSQL"

// psqlScript renders a `sudo -u postgres psql` invocation (local peer auth, no
// password) that runs the given statements against database (or the default database
// when empty) with ON_ERROR_STOP so any failed statement fails the command. The
// statements are fed on stdin via a quoted heredoc so `$$` and literals survive.
func psqlScript(port int, database string, stmts []string) string {
	target := ""
	if database != "" {
		target = " -d " + shQuote(database)
	}
	body := strings.Join(stmts, ";\n") + ";\n"
	return fmt.Sprintf("sudo -u %s psql -p %d -w -v ON_ERROR_STOP=1%s <<'%s'\n%s%s",
		OSUser, port, target, psqlDelimiter, body, psqlDelimiter)
}

// EnsureOwnerScript renders shell that idempotently ensures the database owner role
// exists as a NOLOGIN role (it owns the database and schema objects; nothing connects
// as it — per-service login roles are minted separately, so the owner needs no
// password).
func EnsureOwnerScript(port int, owner string) string {
	return psqlScript(port, "", []string{pgrole.EnsureRoleNoLoginSQL(owner)})
}

// EnsureDatabaseScript renders shell that creates the logical database owned by owner
// if it does not already exist (CREATE DATABASE cannot run inside a transaction/DO
// block, so the existence check + createdb are done at the shell level).
func EnsureDatabaseScript(port int, database, owner string) string {
	check := fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname=%s;", pgrole.QuoteLiteral(database))
	return strings.Join([]string{
		"set -e",
		// A literal heredoc keeps the SQL (its single-quoted literal) intact, unlike an
		// inline -c arg which would need nested shell-quote escaping.
		fmt.Sprintf(`if [ "$(sudo -u %s psql -p %d -w -tA <<'%s'`, OSUser, port, psqlDelimiter),
		check,
		psqlDelimiter,
		`)" != "1" ]; then`,
		fmt.Sprintf(`  sudo -u %s createdb -p %d -O %s %s`, OSUser, port, shQuote(owner), shQuote(database)),
		"fi",
	}, "\n")
}

// MintRoleScript renders shell that mints (create-or-update) a per-service LOGIN role
// with password and applies its ro/rw GRANTs on database — the self-hosted analogue of
// the Neon RoleProvisioner, run on the host over local peer auth. It runs connected to
// database (psql -d), which the per-database statements (REASSIGN OWNED, the
// schema-public grants) depend on. owner is the database's NOLOGIN owner role, the
// target of the downgrade reassign. The password appears in the rendered SQL (quoted),
// so the caller wraps the whole command as a Pulumi secret. Returns an error for an
// unknown permission.
func MintRoleScript(port int, role, password, database, owner, permission string) (string, error) {
	stmts, err := pgrole.MintRoleSQL(role, password, database, owner, permission)
	if err != nil {
		return "", err
	}
	return psqlScript(port, database, stmts), nil
}

// MintMonitorRoleScript renders shell that mints (create-or-update) the observability
// monitoring LOGIN role with password, grants it the built-in pg_monitor role, and
// grants CONNECT on each scraped database (ADR-0037). It runs on the host over local
// peer auth from the default database — every statement is cluster-level. The password
// appears in the rendered SQL (quoted), so the caller wraps the whole command as a
// Pulumi secret. Teardown reuses DropRoleScript (the monitor role owns no objects, so
// its DROP OWNED just revokes the pg_monitor membership + CONNECT grants).
func MintMonitorRoleScript(port int, role, password string, databases []string) string {
	return psqlScript(port, "", pgrole.MintMonitorRoleSQL(role, password, databases))
}

// DropRoleScript renders shell that reassigns the role's owned objects to owner, drops
// its privileges, then drops the role — run at role teardown over local peer auth.
//
// It connects to database, and that is load-bearing: REASSIGN OWNED and DROP OWNED are
// PER-DATABASE (they only see the current database's catalog, plus shared objects), so
// running them from the cluster's default `postgres` database leaves every object and
// ACL the role owns in its own database untouched — and the DROP ROLE that follows then
// fails with `role "…" cannot be dropped because some objects depend on it`, failing the
// teardown (and, since the mint command is DeleteBeforeReplace, any re-mint that
// replaces it, e.g. an rw→ro permission change). A per-service role is bound to exactly
// one database, so that database is the only catalog it can hold anything in. DROP ROLE
// itself is cluster-wide and runs fine from there.
//
// database may be empty for a role that is scoped to no single database and owns nothing
// — the monitor role (ADR-0037), whose pg_monitor membership and CONNECT grants are
// cluster-level / shared-object privileges that DROP OWNED reaches from any database.
func DropRoleScript(port int, role, owner, database string) string {
	stmts := append(pgrole.ReassignDropSQL(role, owner), pgrole.DropRoleSQL(role))
	return psqlScript(port, database, stmts)
}
