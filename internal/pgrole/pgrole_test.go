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
	if len(ro) != 6 {
		t.Fatalf("ro: got %d statements, want 6", len(ro))
	}
	if ro[0] != `GRANT CONNECT ON DATABASE "app" TO "svc"` {
		t.Errorf("ro[0] = %q", ro[0])
	}
	for _, s := range ro {
		if strings.Contains(s, "INSERT") || strings.Contains(s, "CREATE") {
			t.Errorf("ro must not grant write/create: %q", s)
		}
	}

	rw, err := GrantSQL("rw", "svc", "app")
	if err != nil {
		t.Fatalf("rw: %v", err)
	}
	if len(rw) != 6 {
		t.Fatalf("rw: got %d statements, want 6", len(rw))
	}
	var sawCreate bool
	for _, s := range rw {
		if strings.Contains(s, "USAGE, CREATE ON SCHEMA public") {
			sawCreate = true
		}
	}
	if !sawCreate {
		t.Error("rw must grant CREATE ON SCHEMA public")
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
		`CREATE ROLE "svc-x" LOGIN PASSWORD 'p''w'`,
		`ALTER ROLE "svc-x" WITH LOGIN PASSWORD 'p''w'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("CreateRoleLoginSQL missing %q in:\n%s", want, s)
		}
	}
}

func TestMintRoleSQL(t *testing.T) {
	stmts, err := MintRoleSQL("svc", "pw", "app", "rw")
	if err != nil {
		t.Fatalf("MintRoleSQL: %v", err)
	}
	if len(stmts) != 13 { // 1 create + 6 revoke + 6 grants
		t.Fatalf("got %d statements, want 13", len(stmts))
	}
	if !strings.HasPrefix(stmts[0], "DO $$") {
		t.Errorf("first statement should be the create DO block, got %q", stmts[0])
	}
	// The revoke block precedes the grants so a downgrade drops stale privileges.
	if !strings.HasPrefix(stmts[1], "REVOKE ALL PRIVILEGES ON ALL TABLES") {
		t.Errorf("second statement should begin the revoke block, got %q", stmts[1])
	}
	if _, err := MintRoleSQL("svc", "pw", "app", "bogus"); err == nil {
		t.Error("bad permission must propagate an error")
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
		`CREATE ROLE "mon" LOGIN PASSWORD 's3cret'`,
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

func TestEnsureRoleNoLoginSQL(t *testing.T) {
	s := EnsureRoleNoLoginSQL("app-owner")
	for _, want := range []string{
		"IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app-owner')",
		`CREATE ROLE "app-owner" NOLOGIN`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("EnsureRoleNoLoginSQL missing %q in:\n%s", want, s)
		}
	}
}

// CheckGroupRoleNames is validate-only until the group-role redesign re-lands (its
// mint side was reverted for the v6.1.1 state migration — see MintRoleSQL). The
// checks stay live so existing manifests are already clean when the mint starts
// creating the group roles again.
func TestCheckGroupRoleNames(t *testing.T) {
	// The database owner is operator-authored free text. If it collides with a derived
	// group role, `GRANT <writer group> TO <service>` would make the service a member
	// of the role that OWNS the database and every object in it.
	for _, owner := range []string{"app_ro", "app_rw"} {
		if err := CheckGroupRoleNames("svc", "app", owner); err == nil {
			t.Errorf("owner %q must error (owner is a derived group role)", owner)
		}
	}
	if err := CheckGroupRoleNames("app_rw", "app", "app-owner"); err == nil {
		t.Error("a login role named like a group role must error")
	}
	if err := CheckGroupRoleNames("svc", "app", "app-owner"); err != nil {
		t.Errorf("a non-colliding triple must pass: %v", err)
	}
}

// Postgres truncates identifiers at MaxIdentifierLen bytes, silently. The group roles
// are derived from the operator's database: value with a 3-byte suffix, so a database
// name within 3 bytes of the limit renders a group role the server stores under a
// DIFFERENT name — and two databases whose _rw names share a 63-byte prefix collapse
// onto ONE real group role, wiring one database's ALTER DEFAULT PRIVILEGES to the other
// database's grantees. program.checkDBRoleNames applies the same limit to the LOGIN role
// but never sees these derived names, so the check has to live here.
func TestCheckGroupRoleNamesRejectsTruncatedIdentifiers(t *testing.T) {
	// 61 bytes + "_rw" == 64 > 63: the writer group would be truncated.
	long := strings.Repeat("d", MaxIdentifierLen-2)
	if err := CheckGroupRoleNames("svc", long, "app"); err == nil {
		t.Errorf("database %q derives the %d-byte group role %q; must be rejected", long, len(WriterGroup(long)), WriterGroup(long))
	}
	// 60 bytes + "_rw" == 63: exactly at the limit, still fine.
	fits := strings.Repeat("d", MaxIdentifierLen-3)
	if err := CheckGroupRoleNames("svc", fits, "app"); err != nil {
		t.Errorf("database %q derives a group role of exactly %d bytes and must be accepted: %v", fits, MaxIdentifierLen, err)
	}
	// The owner is a real CREATE ROLE / createdb -O target and truncates the same way.
	if err := CheckGroupRoleNames("svc", "app", strings.Repeat("o", MaxIdentifierLen+1)); err == nil {
		t.Error("an over-long owner role must be rejected")
	}
}
