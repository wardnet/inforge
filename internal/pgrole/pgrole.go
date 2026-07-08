// Package pgrole renders the Postgres SQL statements that provision a scoped
// per-service database role and its ro/rw GRANTs (ADR-0025, ADR-0036). It is pure
// and transport-neutral: it builds statement TEXT only, with no database driver and
// no Pulumi/provider dependency. The self-hosted Postgres path renders the statements
// into an on-host `psql` invocation (a private-only cluster is unreachable from the
// deploy machine, so role minting runs on the host over local peer auth; see
// ADR-0036). The now-retired Neon provider ran the same text over pgx from the deploy
// machine — the transport-neutrality is why the SQL lives here, apart from either.
//
// Identifiers are quoted with QuoteIdent and string literals with QuoteLiteral, so a
// role/database name or a generated password composes safely into the statements.
package pgrole

import (
	"fmt"
	"strings"
)

// QuoteIdent safely quotes a Postgres identifier (role/database/schema name) by
// wrapping it in double quotes and doubling any embedded double quote — the standard
// delimited-identifier escaping. A NUL byte is stripped (Postgres identifiers cannot
// contain one) rather than allowed to truncate the statement.
func QuoteIdent(ident string) string {
	ident = strings.ReplaceAll(ident, "\x00", "")
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// QuoteLiteral quotes a Postgres string literal (e.g. a generated password) by
// wrapping it in single quotes and doubling any embedded single quote. This is safe
// under standard_conforming_strings=on (the default since Postgres 9.1), where a
// backslash is an ordinary character; a NUL byte — illegal in a Postgres string — is
// stripped so it cannot terminate the literal early.
func QuoteLiteral(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// GrantSQL returns the ordered statements that grant role the ro/rw privileges on
// schema public of database, run as the database owner (or a superuser). ALTER
// DEFAULT PRIVILEGES covers tables/sequences the owner creates later. rw additionally
// grants CREATE on the schema so the service can run its own migrations; ro is
// read-only. An unknown permission is an error.
func GrantSQL(permission, role, database string) ([]string, error) {
	r := QuoteIdent(role)
	db := QuoteIdent(database)
	switch permission {
	case "ro":
		return []string{
			fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, db, r),
			fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO %s`, r),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON SEQUENCES TO %s`, r),
		}, nil
	case "rw":
		return []string{
			fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, db, r),
			fmt.Sprintf(`GRANT USAGE, CREATE ON SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s`, r),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %s`, r),
		}, nil
	default:
		return nil, fmt.Errorf("pgrole: unknown grant permission %q (want ro or rw)", permission)
	}
}

// CreateRoleLoginSQL returns an idempotent statement that ensures a LOGIN role named
// role exists with the given password: it creates the role if absent, else resets its
// password (the password is stable across deploys, so a re-run converges without
// rotating it). It is a single anonymous DO block so it is safe to re-run.
func CreateRoleLoginSQL(role, password string) string {
	return fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s LOGIN PASSWORD %s;
  ELSE
    ALTER ROLE %s WITH LOGIN PASSWORD %s;
  END IF;
END
$$`, QuoteLiteral(role), QuoteIdent(role), QuoteLiteral(password), QuoteIdent(role), QuoteLiteral(password))
}

// RevokeAllSQL returns the statements that strip every privilege a per-service role
// currently holds on database — its schema-public table/sequence privileges, the
// schema and database privileges, and the default privileges for future objects. It
// is run before re-applying the current permission's GRANTs so a re-mint is fully
// declarative: downgrading a grant from rw to ro actually drops the write privileges
// instead of leaving them in place (a REVOKE for a privilege the role never held is a
// harmless no-op). Run as the database owner / superuser.
func RevokeAllSQL(role, database string) []string {
	r := QuoteIdent(role)
	db := QuoteIdent(database)
	return []string{
		fmt.Sprintf(`REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM %s`, r),
		fmt.Sprintf(`REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM %s`, r),
		fmt.Sprintf(`REVOKE ALL PRIVILEGES ON SCHEMA public FROM %s`, r),
		fmt.Sprintf(`REVOKE ALL PRIVILEGES ON DATABASE %s FROM %s`, db, r),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM %s`, r),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM %s`, r),
	}
}

// MintRoleSQL is the full self-hosted role-provisioning statement list: ensure the
// LOGIN role exists with password, revoke every privilege it currently holds, then
// apply its ro/rw GRANTs on database. The revoke-then-grant makes the mint
// declarative — a permission downgrade (rw→ro) drops the stale write grants rather
// than accumulating privileges across deploys. It is the unit the on-host psql script
// executes for a grant (ADR-0036).
func MintRoleSQL(role, password, database, permission string) ([]string, error) {
	grants, err := GrantSQL(permission, role, database)
	if err != nil {
		return nil, err
	}
	stmts := []string{CreateRoleLoginSQL(role, password)}
	stmts = append(stmts, RevokeAllSQL(role, database)...)
	return append(stmts, grants...), nil
}

// MintMonitorRoleSQL is the full statement list for the observability monitoring role
// (ADR-0037): ensure the LOGIN role exists with password, grant it the built-in
// `pg_monitor` role (read access to all statistics/monitoring views, incl.
// pg_read_all_stats), and grant CONNECT on each database the collector scrapes. It is
// deliberately READ-ONLY and distinct from MintRoleSQL: no schema/table grants, no
// revoke-all — a monitoring role only reads stats views, never user data. It is run
// (as a superuser, over local peer auth) once per cluster; the GRANTs are cluster-level
// so it executes fine from the default `postgres` database. databases should be the
// metrics-enabled set for the cluster.
func MintMonitorRoleSQL(role, password string, databases []string) []string {
	r := QuoteIdent(role)
	stmts := []string{
		CreateRoleLoginSQL(role, password),
		// pg_monitor is a fixed predefined role name (not user input) — left unquoted.
		fmt.Sprintf(`GRANT pg_monitor TO %s`, r),
	}
	for _, db := range databases {
		stmts = append(stmts, fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, QuoteIdent(db), r))
	}
	return stmts
}

// ReassignDropSQL returns the ordered cleanup statements to run (as the owner or a
// superuser) before dropping role: reassign objects it owns to owner, then drop the
// privileges it holds. It is used at role teardown.
func ReassignDropSQL(role, owner string) []string {
	r := QuoteIdent(role)
	return []string{
		fmt.Sprintf(`REASSIGN OWNED BY %s TO %s`, r, QuoteIdent(owner)),
		fmt.Sprintf(`DROP OWNED BY %s`, r),
	}
}

// DropRoleSQL drops role if it exists (run after ReassignDropSQL).
func DropRoleSQL(role string) string {
	return fmt.Sprintf(`DROP ROLE IF EXISTS %s`, QuoteIdent(role))
}
