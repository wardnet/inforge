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

// ReaderGroup and WriterGroup name the two NOLOGIN group roles that carry a database's
// ro/rw privileges on objects created after a mint. They exist because ALTER DEFAULT
// PRIVILEGES entries are keyed to BOTH the creating role (defaclrole) and the grantee:
// the tables of a service are created by ITS rw login role (it runs the migrations and
// must own them to keep DDL), while the grantees are other services' login roles that
// may be minted before or after it. Naming each side of that pair after the database —
// the one identity both mints share — makes the two mints order-independent: an rw mint
// declares its defaults for the groups, an ro mint just joins the reader group.
func ReaderGroup(database string) string { return database + "_ro" }
func WriterGroup(database string) string { return database + "_rw" }

// EnsureGroupRolesSQL returns idempotent statements creating a database's reader and
// writer group roles (NOLOGIN, own nothing, hold no password — pure privilege carriers).
func EnsureGroupRolesSQL(database string) []string {
	return []string{
		EnsureRoleNoLoginSQL(ReaderGroup(database)),
		EnsureRoleNoLoginSQL(WriterGroup(database)),
	}
}

// EnsureRoleNoLoginSQL returns an idempotent statement that ensures a NOLOGIN role
// exists — the shape shared by the database owner role and the two group roles: it
// carries privileges/ownership but nothing ever connects as it, so it needs no
// password. It is a single anonymous DO block, safe to re-run.
func EnsureRoleNoLoginSQL(role string) string {
	return fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s NOLOGIN;
  END IF;
END
$$`, QuoteLiteral(role), QuoteIdent(role))
}

// MaxIdentifierLen is Postgres's NAMEDATALEN-1: the longest identifier the server
// stores. A longer name is SILENTLY TRUNCATED to this many bytes at CREATE ROLE / CREATE
// DATABASE — even when double-quoted — so a too-long name is never the name the
// deployment then uses, and two names sharing a MaxIdentifierLen-byte prefix collapse
// onto ONE real object. Every identifier inforge derives is length-checked against it.
const MaxIdentifierLen = 63

// CheckGroupRoleNames rejects a (role, database, owner) triple whose derived group-role
// names are unusable — either too long for Postgres to store as written, or colliding
// with the login role or the database owner.
//
// Collision: the database owner is operator-authored free text (database/<name>'s
// owner:), so nothing stops it being named `<database>_rw` — and then the mint's
// `GRANT <writer-group> TO <login role>` would quietly make the service a member of the
// role that OWNS the database and every object in it (DROP DATABASE, DROP TABLE),
// collapsing the ro/rw split into superuser-of-this-database.
//
// Length: the group roles are derived from the operator's `database:` value with a
// 3-byte suffix, so a database name within 3 bytes of MaxIdentifierLen renders a group
// role Postgres truncates. Two databases whose `_rw` names share a 63-byte prefix then
// collapse onto ONE real group role, and the ALTER DEFAULT PRIVILEGES each rw mint
// declares for "its" group would feed the other database's grantees — the same
// truncation bug class program.checkDBRoleNames closes for the LOGIN role, which does
// not see these derived names. (The owner is checked too: it is a real CREATE ROLE
// target, and a truncated owner is not the role `createdb -O` then names.)
//
// `inforge validate` reports all of this credential-free. Until the group-role
// redesign re-lands (it was reverted for the v6.1.1 state migration — see MintRoleSQL),
// validate is the only caller: the names are pre-validated so existing manifests are
// already clean when the mint starts creating the group roles again.
func CheckGroupRoleNames(role, database, owner string) error {
	if n := len(owner); n > MaxIdentifierLen {
		return fmt.Errorf("pgrole: database %q: owner role %q is %d bytes; Postgres truncates identifiers at %d, so it would create a role of a different name than the deployment uses — shorten it by %d character(s)", database, owner, n, MaxIdentifierLen, n-MaxIdentifierLen)
	}
	for _, group := range []string{ReaderGroup(database), WriterGroup(database)} {
		if n := len(group); n > MaxIdentifierLen {
			return fmt.Errorf("pgrole: database %q: the derived group role %q is %d bytes; Postgres truncates identifiers at %d, so two databases whose group names share a %d-byte prefix would collapse onto one role — shorten the database name by %d character(s)", database, group, n, MaxIdentifierLen, MaxIdentifierLen, n-MaxIdentifierLen)
		}
		if owner == group {
			return fmt.Errorf("pgrole: database %q: owner role %q collides with the derived group role of the same name; rename the owner (a service granted rw would inherit ownership of the database)", database, owner)
		}
		if role == group {
			return fmt.Errorf("pgrole: database %q: login role %q collides with the derived group role of the same name", database, role)
		}
	}
	return nil
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
//
// DO NOT CHANGE THE RENDERED STATEMENTS in this release line (v6.1.x). The rendered
// mint script is the `-mint` remote.Command's Triggers input, so any byte change
// forces a Pulumi REPLACE — and DeleteBeforeReplace then runs the delete script
// RECORDED IN STATE by the PREVIOUS deploy, which for every pre-v6.1.1 stack is the
// broken pre-#225 drop (no per-database clears) that fails `DROP ROLE` and aborts the
// deploy mid-flight (the v6.1.0 production outage). v6.1.1 exists to refresh that
// recorded delete via a pure `[diff: ~delete]` UPDATE, which requires this script to
// stay byte-identical to v6.0.0's — enforced by TestMintScriptsByteIdenticalToV600.
// The group-role redesign (#225's mint side) re-lands only after Triggers are retired
// from the mint commands, when a script change is an in-place update.
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
