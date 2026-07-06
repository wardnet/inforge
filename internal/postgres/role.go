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
	do := fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s NOLOGIN;
  END IF;
END
$$`, pgrole.QuoteLiteral(owner), pgrole.QuoteIdent(owner))
	return psqlScript(port, "", []string{do})
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
// the Neon RoleProvisioner, run on the host over local peer auth. The password appears
// in the rendered SQL (quoted), so the caller wraps the whole command as a Pulumi
// secret. Returns an error for an unknown permission.
func MintRoleScript(port int, role, password, database, permission string) (string, error) {
	stmts, err := pgrole.MintRoleSQL(role, password, database, permission)
	if err != nil {
		return "", err
	}
	return psqlScript(port, database, stmts), nil
}

// DropRoleScript renders shell that reassigns the role's owned objects to owner, drops
// its privileges, then drops the role — run at role teardown over local peer auth.
func DropRoleScript(port int, role, owner string) string {
	stmts := append(pgrole.ReassignDropSQL(role, owner), pgrole.DropRoleSQL(role))
	return psqlScript(port, "", stmts)
}
