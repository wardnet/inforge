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
// `inforge validate` reports all of this credential-free; the mint calls it as the
// enforcement point, so a deploy that skipped validation still fails closed.
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
// schema public of database, run as a superuser (the self-hosted mint connects as
// `postgres` over local peer auth). Beyond the privileges on the objects that exist
// today, the role joins the database's reader (ro) or writer (rw) group; an rw role
// additionally declares ALTER DEFAULT PRIVILEGES FOR ROLE <itself> — the only correct
// defaclrole, since it is the role that creates the service's tables — so every table
// and sequence it creates later reaches both groups. rw grants CREATE on the schema so
// the service can run its own migrations; ro is read-only. An unknown permission is an
// error.
func GrantSQL(permission, role, database string) ([]string, error) {
	r := QuoteIdent(role)
	db := QuoteIdent(database)
	ro := QuoteIdent(ReaderGroup(database))
	rw := QuoteIdent(WriterGroup(database))
	switch permission {
	case "ro":
		return []string{
			fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, db, r),
			fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT %s TO %s`, ro, r),
			fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO %s`, r),
		}, nil
	case "rw":
		return []string{
			fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, db, r),
			fmt.Sprintf(`GRANT USAGE, CREATE ON SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT %s TO %s`, rw, r),
			fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO %s`, r),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT ON TABLES TO %s`, r, ro),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT ON SEQUENCES TO %s`, r, ro),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s`, r, rw),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %s`, r, rw),
		}, nil
	default:
		return nil, fmt.Errorf("pgrole: unknown grant permission %q (want ro or rw)", permission)
	}
}

// CreateRoleLoginSQL returns an idempotent statement that ensures a LOGIN role named
// role exists with the given password: it creates the role if absent, else resets its
// password (the password is stable across deploys, so a re-run converges without
// rotating it). It is a single anonymous DO block so it is safe to re-run.
//
// INHERIT is stated explicitly on both branches, not left to the CREATE ROLE default:
// every privilege this role holds on objects created after its mint arrives through
// membership of a group role (the database's reader/writer groups; pg_monitor for the
// collector role), and a NOINHERIT role holds those privileges only after an explicit
// SET ROLE — which no service issues. A role that reached NOINHERIT any other way (an
// operator ALTER ROLE, a restored cluster) would silently read zero rows; the mint
// reconciles it back.
func CreateRoleLoginSQL(role, password string) string {
	return fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s LOGIN INHERIT PASSWORD %s;
  ELSE
    ALTER ROLE %s WITH LOGIN INHERIT PASSWORD %s;
  END IF;
END
$$`, QuoteLiteral(role), QuoteIdent(role), QuoteLiteral(password), QuoteIdent(role), QuoteLiteral(password))
}

// RevokeAllSQL returns the statements that strip every privilege a per-service role
// currently holds on database — its schema-public table/sequence privileges, the schema
// and database privileges, its membership of the database's ro/rw group roles, and the
// default privileges for future objects (both the entries keyed to the role itself and
// the historical, mis-keyed superuser entries earlier inforge versions minted). It is
// run before re-applying the current permission's GRANTs so a re-mint is fully
// declarative: downgrading a grant from rw to ro actually drops the write privileges
// instead of leaving them in place (a REVOKE for a privilege the role never held is a
// harmless no-op). It cannot reach privileges the role holds by OWNERSHIP — see
// MintRoleSQL, which reassigns those first. Run as a superuser.
func RevokeAllSQL(role, database string) []string {
	r := QuoteIdent(role)
	db := QuoteIdent(database)
	ro := QuoteIdent(ReaderGroup(database))
	rw := QuoteIdent(WriterGroup(database))
	return []string{
		fmt.Sprintf(`REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM %s`, r),
		fmt.Sprintf(`REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM %s`, r),
		fmt.Sprintf(`REVOKE ALL PRIVILEGES ON SCHEMA public FROM %s`, r),
		fmt.Sprintf(`REVOKE ALL PRIVILEGES ON DATABASE %s FROM %s`, db, r),
		fmt.Sprintf(`REVOKE %s, %s FROM %s`, ro, rw, r),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM %s`, r),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM %s`, r),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public REVOKE ALL ON TABLES FROM %s, %s`, r, ro, rw),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public REVOKE ALL ON SEQUENCES FROM %s, %s`, r, ro, rw),
	}
}

// MintRoleSQL is the full self-hosted role-provisioning statement list for one grant,
// executed against database (the on-host psql script connects with `-d database`, which
// is what makes the per-database statements below — the reassign and the schema-public
// grants — hit the right catalog). It ensures the database's ro/rw group roles and the
// LOGIN role exist, reconciles ownership, revokes every privilege the role currently
// holds, then applies its ro/rw GRANTs. The revoke-then-grant makes the mint declarative
// — a permission downgrade (rw→ro) drops the stale write grants rather than accumulating
// privileges across deploys (ADR-0036).
//
// A downgraded role additionally has the objects it owns REASSIGNed to owner (the
// database's NOLOGIN owner role) BEFORE the revokes: ownership privileges are implicit
// and unreachable by REVOKE, so an ex-rw role would otherwise keep full write + DDL on
// every table it ever created. The reassign runs first so the following REVOKEs also
// clear any explicit ACL entries left on those objects, and the GRANTs then re-add
// exactly the ro set. An rw role is NOT reassigned: it must own its tables to run its
// own migrations (ALTER/DROP require ownership).
//
// The reassign is a ONE-WAY door, deliberately: the objects land on the NOLOGIN owner
// role, which no login role is a member of, so a grant flipped back rw→ro→rw leaves the
// restored rw role with DML (via the writer group) and CREATE on schema public but
// unable to ALTER/DROP the tables it used to own — its migrations will fail loudly on
// the first ALTER. That is the intended trade: an ro grant that quietly retains write +
// DDL is a security hole, while a re-upgrade that needs a one-off `ALTER TABLE … OWNER
// TO <role>` is a visible, recoverable operation.
func MintRoleSQL(role, password, database, owner, permission string) ([]string, error) {
	grants, err := GrantSQL(permission, role, database)
	if err != nil {
		return nil, err
	}
	if err := CheckGroupRoleNames(role, database, owner); err != nil {
		return nil, err
	}
	if permission == "ro" && owner == "" {
		return nil, fmt.Errorf("pgrole: role %q: ro mint needs the database owner to reassign owned objects to", role)
	}
	stmts := EnsureGroupRolesSQL(database)
	stmts = append(stmts, CreateRoleLoginSQL(role, password))
	if permission == "ro" {
		stmts = append(stmts, fmt.Sprintf(`REASSIGN OWNED BY %s TO %s`, QuoteIdent(role), QuoteIdent(owner)))
	}
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
