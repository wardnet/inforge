package pgrole

import (
	"strings"
	"testing"
)

func TestQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"app":        `"app"`,
		`we"ird`:     `"we""ird"`,
		"svc-tunnel": `"svc-tunnel"`,
		"a\x00b":     `"ab"`, // NUL stripped
		`"; DROP --`: `"""; DROP --"`,
	}
	for in, want := range cases {
		if got := QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	cases := map[string]string{
		"hunter2":     `'hunter2'`,
		"o'brien":     `'o''brien'`,
		`back\slash`:  `'back\slash'`, // backslash literal (standard_conforming_strings)
		"a\x00b":      `'ab'`,
		"' OR 1=1 --": `''' OR 1=1 --'`,
	}
	for in, want := range cases {
		if got := QuoteLiteral(in); got != want {
			t.Errorf("QuoteLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGrantSQL(t *testing.T) {
	ro, err := GrantSQL("ro", "svc", "app")
	if err != nil {
		t.Fatalf("ro: %v", err)
	}
	if len(ro) != 5 {
		t.Fatalf("ro: got %d statements, want 5", len(ro))
	}
	if ro[0] != `GRANT CONNECT ON DATABASE "app" TO "svc"` {
		t.Errorf("ro[0] = %q", ro[0])
	}
	for _, s := range ro {
		if strings.Contains(s, "INSERT") || strings.Contains(s, "CREATE") {
			t.Errorf("ro must not grant write/create: %q", s)
		}
	}
	// A reader never creates objects, so it declares no default privileges; it reads
	// future tables through the database's reader group.
	joinedRO := strings.Join(ro, "\n")
	if strings.Contains(joinedRO, "ALTER DEFAULT PRIVILEGES") {
		t.Errorf("ro must declare no default privileges:\n%s", joinedRO)
	}
	if !strings.Contains(joinedRO, `GRANT "app_ro" TO "svc"`) {
		t.Errorf("ro must join the database reader group:\n%s", joinedRO)
	}

	rw, err := GrantSQL("rw", "svc", "app")
	if err != nil {
		t.Fatalf("rw: %v", err)
	}
	if len(rw) != 9 {
		t.Fatalf("rw: got %d statements, want 9", len(rw))
	}
	joinedRW := strings.Join(rw, "\n")
	// The rw role creates the service's tables, so IT must be the defaclrole — an
	// ALTER DEFAULT PRIVILEGES without FOR ROLE keys the entry to the connected
	// superuser and future tables reach no grantee at all.
	for _, want := range []string{
		`GRANT USAGE, CREATE ON SCHEMA public TO "svc"`,
		`GRANT "app_rw" TO "svc"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "svc" IN SCHEMA public GRANT SELECT ON TABLES TO "app_ro"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "svc" IN SCHEMA public GRANT SELECT ON SEQUENCES TO "app_ro"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "svc" IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "app_rw"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "svc" IN SCHEMA public GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO "app_rw"`,
	} {
		if !strings.Contains(joinedRW, want) {
			t.Errorf("rw missing %q in:\n%s", want, joinedRW)
		}
	}
	for _, s := range rw {
		if strings.HasPrefix(s, "ALTER DEFAULT PRIVILEGES IN SCHEMA") {
			t.Errorf("default privileges must name the creating role via FOR ROLE: %q", s)
		}
	}

	if _, err := GrantSQL("admin", "svc", "app"); err == nil {
		t.Error("unknown permission must error")
	}
}

func TestCreateRoleLoginSQL(t *testing.T) {
	s := CreateRoleLoginSQL("svc-x", "p'w")
	// Idempotent shape: guarded create, else alter.
	for _, want := range []string{
		"IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'svc-x')",
		// INHERIT is explicit on BOTH branches: every privilege on post-mint objects
		// arrives through group membership (the db reader/writer groups, pg_monitor), and
		// a NOINHERIT role would hold none of it without a SET ROLE no service issues.
		`CREATE ROLE "svc-x" LOGIN INHERIT PASSWORD 'p''w'`,
		`ALTER ROLE "svc-x" WITH LOGIN INHERIT PASSWORD 'p''w'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("CreateRoleLoginSQL missing %q in:\n%s", want, s)
		}
	}
}

// The database owner is operator-authored free text. If it collides with a derived group
// role, `GRANT <writer group> TO <service>` would make the service a member of the role
// that OWNS the database and every object in it.
func TestMintRoleSQLRejectsOwnerCollidingWithGroupRole(t *testing.T) {
	for _, owner := range []string{"app_ro", "app_rw"} {
		for _, perm := range []string{"ro", "rw"} {
			if _, err := MintRoleSQL("svc", "pw", "app", owner, perm); err == nil {
				t.Errorf("%s mint with owner %q must error (owner is a derived group role)", perm, owner)
			}
		}
	}
	if err := CheckGroupRoleNames("app_rw", "app", "app-owner"); err == nil {
		t.Error("a login role named like a group role must error")
	}
	if err := CheckGroupRoleNames("svc", "app", "app-owner"); err != nil {
		t.Errorf("a non-colliding triple must pass: %v", err)
	}
}

func TestMintRoleSQL(t *testing.T) {
	stmts, err := MintRoleSQL("svc", "pw", "app", "app-owner", "rw")
	if err != nil {
		t.Fatalf("MintRoleSQL: %v", err)
	}
	if len(stmts) != 21 { // 2 group + 1 create + 9 revoke + 9 grants
		t.Fatalf("got %d statements, want 21", len(stmts))
	}
	if !strings.Contains(stmts[0], `CREATE ROLE "app_ro" NOLOGIN`) || !strings.Contains(stmts[1], `CREATE ROLE "app_rw" NOLOGIN`) {
		t.Errorf("mint must ensure the database group roles first, got %q, %q", stmts[0], stmts[1])
	}
	if !strings.Contains(stmts[2], `CREATE ROLE "svc" LOGIN`) {
		t.Errorf("third statement should be the login-role DO block, got %q", stmts[2])
	}
	// The revoke block precedes the grants so a downgrade drops stale privileges.
	if !strings.HasPrefix(stmts[3], "REVOKE ALL PRIVILEGES ON ALL TABLES") {
		t.Errorf("revoke block should follow the create, got %q", stmts[3])
	}
	// An rw role owns the tables it creates (it runs the migrations): reassigning them
	// away would strip its DDL rights.
	for _, s := range stmts {
		if strings.HasPrefix(s, "REASSIGN OWNED") {
			t.Errorf("rw mint must not reassign owned objects: %q", s)
		}
	}

	if _, err := MintRoleSQL("svc", "pw", "app", "app-owner", "bogus"); err == nil {
		t.Error("bad permission must propagate an error")
	}
	if _, err := MintRoleSQL("svc", "pw", "app", "", "ro"); err == nil {
		t.Error("ro mint without a database owner must error (nothing to reassign to)")
	}
}

// A downgraded rw→ro role keeps write + DDL on every table it created unless its
// OWNERSHIP is reassigned away: REVOKE cannot reach an owner's implicit privileges.
func TestMintRoleSQLDowngradeReassignsOwnershipBeforeRevoke(t *testing.T) {
	stmts, err := MintRoleSQL("svc", "pw", "app", "app-owner", "ro")
	if err != nil {
		t.Fatalf("MintRoleSQL: %v", err)
	}
	reassign, firstRevoke, firstGrant := -1, -1, -1
	for i, s := range stmts {
		switch {
		case s == `REASSIGN OWNED BY "svc" TO "app-owner"` && reassign < 0:
			reassign = i
		case strings.HasPrefix(s, "REVOKE") && firstRevoke < 0:
			firstRevoke = i
		case strings.HasPrefix(s, "GRANT") && firstGrant < 0:
			firstGrant = i
		}
	}
	if reassign < 0 {
		t.Fatalf("ro mint must reassign owned objects to the database owner:\n%s", strings.Join(stmts, "\n"))
	}
	if firstRevoke < 0 || firstGrant < 0 {
		t.Fatalf("expected revoke and grant blocks:\n%s", strings.Join(stmts, "\n"))
	}
	if reassign >= firstRevoke || firstRevoke >= firstGrant {
		t.Errorf("want REASSIGN (%d) < REVOKE (%d) < GRANT (%d):\n%s", reassign, firstRevoke, firstGrant, strings.Join(stmts, "\n"))
	}
}

func TestRevokeAllSQL(t *testing.T) {
	joined := strings.Join(RevokeAllSQL("svc", "app"), "\n")
	for _, want := range []string{
		`REVOKE ALL PRIVILEGES ON DATABASE "app" FROM "svc"`,
		// Group membership carries the privileges on post-mint objects — a downgrade
		// that left the writer membership in place would keep granting write.
		`REVOKE "app_ro", "app_rw" FROM "svc"`,
		// The role's own default-privilege entries (defaclrole = the role), plus the
		// mis-keyed superuser entries older inforge versions minted.
		`ALTER DEFAULT PRIVILEGES FOR ROLE "svc" IN SCHEMA public REVOKE ALL ON TABLES FROM "app_ro", "app_rw"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "svc" IN SCHEMA public REVOKE ALL ON SEQUENCES FROM "app_ro", "app_rw"`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM "svc"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("RevokeAllSQL missing %q in:\n%s", want, joined)
		}
	}
}

func TestReassignDropAndDrop(t *testing.T) {
	c := ReassignDropSQL("svc", "app")
	if c[0] != `REASSIGN OWNED BY "svc" TO "app"` || c[1] != `DROP OWNED BY "svc"` {
		t.Errorf("ReassignDropSQL = %v", c)
	}
	if DropRoleSQL("svc") != `DROP ROLE IF EXISTS "svc"` {
		t.Errorf("DropRoleSQL = %q", DropRoleSQL("svc"))
	}
}

func TestMintMonitorRoleSQL(t *testing.T) {
	stmts := MintMonitorRoleSQL("mon", "s3cret", []string{"tenants", "audit"})
	joined := strings.Join(stmts, "\n")

	// Idempotent LOGIN role with the password, pg_monitor membership, and CONNECT on
	// each scraped database — and NOTHING else (no schema/table grants, no revoke-all).
	for _, want := range []string{
		`CREATE ROLE "mon" LOGIN INHERIT PASSWORD 's3cret'`,
		`GRANT pg_monitor TO "mon"`,
		`GRANT CONNECT ON DATABASE "tenants" TO "mon"`,
		`GRANT CONNECT ON DATABASE "audit" TO "mon"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("MintMonitorRoleSQL missing %q\n%s", want, joined)
		}
	}
	for _, absent := range []string{"REVOKE ALL", "ON SCHEMA public", "ON ALL TABLES"} {
		if strings.Contains(joined, absent) {
			t.Errorf("monitor role must be read-only; unexpected %q\n%s", absent, joined)
		}
	}
}
