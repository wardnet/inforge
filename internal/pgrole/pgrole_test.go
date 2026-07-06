package pgrole

import (
	"strings"
	"testing"
)

func TestQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"app":          `"app"`,
		`we"ird`:       `"we""ird"`,
		"svc-tunnel":   `"svc-tunnel"`,
		"a\x00b":       `"ab"`, // NUL stripped
		`"; DROP --`:   `"""; DROP --"`,
	}
	for in, want := range cases {
		if got := QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	cases := map[string]string{
		"hunter2":       `'hunter2'`,
		"o'brien":       `'o''brien'`,
		`back\slash`:    `'back\slash'`, // backslash literal (standard_conforming_strings)
		"a\x00b":        `'ab'`,
		"' OR 1=1 --":   `''' OR 1=1 --'`,
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
