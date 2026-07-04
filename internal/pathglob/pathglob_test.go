package pathglob

import (
	"strings"
	"testing"
)

func TestParseAndRegex(t *testing.T) {
	cases := []struct {
		glob  string
		regex string
	}{
		{"/status", "^/status$"},
		{"/v1/account", "^/v1/account$"},
		{"/v*/account/**", `^/v[^/]+/account(/.*)?$`},
		{"/tenants/**", `^/tenants(/.*)?$`},
		{"/a-b_c.d/e", `^/a-b_c\.d/e$`},
		{"/*", `^/[^/]+$`},
		{"/v*x*/y", `^/v[^/]+x[^/]+/y$`},
	}
	for _, c := range cases {
		p, err := Parse(c.glob)
		if err != nil {
			t.Errorf("Parse(%q): unexpected error %v", c.glob, err)
			continue
		}
		if got := p.Regex(); got != c.regex {
			t.Errorf("Parse(%q).Regex() = %q, want %q", c.glob, got, c.regex)
		}
		if p.String() != c.glob {
			t.Errorf("Parse(%q).String() = %q", c.glob, p.String())
		}
	}
}

func TestParseRejections(t *testing.T) {
	cases := []struct {
		glob    string
		errPart string
	}{
		{"", "non-root absolute path"},
		{"/", "non-root absolute path"},
		{"v1/account", `must start with "/"`},
		{"/v1/", `must not end with "/"`},
		{"/v1//account", "empty path segment"},
		{"/v1/../admin", `".." segments`},
		{"/./x", `"." segments`},
		{"/a/**/b", `must be the final path segment`},
		{"/**", "matches every request path"},
		{"/a/x**", "stand alone"},
		{"/a/***", "stand alone"},
		{"/a b/c", `contains " "`},
		{"/a/{x}", `contains "{"`},
		{"/a\t/c", "contains"},
	}
	for _, c := range cases {
		_, err := Parse(c.glob)
		if err == nil {
			t.Errorf("Parse(%q): expected error, got none", c.glob)
			continue
		}
		if !strings.Contains(err.Error(), c.errPart) {
			t.Errorf("Parse(%q) error = %q, want it to contain %q", c.glob, err, c.errPart)
		}
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		glob  string
		path  string
		match bool
	}{
		{"/status", "/status", true},
		{"/status", "/status/x", false},
		{"/v*/account/**", "/v1/account", true},
		{"/v*/account/**", "/v1/account/42/emails", true},
		{"/v*/account/**", "/v/account", false},
		{"/v*/account/**", "/v1/accounts", false},
		{"/tenants/**", "/tenants", true},
		{"/tenants/**", "/tenants/", true},
		{"/tenants/**", "/tenantsx", false},
		{"/*", "/healthz", true},
		{"/*", "/a/b", false},
	}
	for _, c := range cases {
		p, err := Parse(c.glob)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.glob, err)
		}
		if got := p.Matches(c.path); got != c.match {
			t.Errorf("Parse(%q).Matches(%q) = %v, want %v", c.glob, c.path, got, c.match)
		}
	}
}

func TestCheckExact(t *testing.T) {
	for _, ok := range []string{"/healthz", "/v1/livez", "/a-b_c.d/e"} {
		if err := CheckExact(ok); err != nil {
			t.Errorf("CheckExact(%q): unexpected error %v", ok, err)
		}
	}
	cases := []struct {
		path    string
		errPart string
	}{
		{"", "non-root absolute path"},
		{"/", "non-root absolute path"},
		{"healthz", `must start with "/"`},
		{"/healthz/", `must not end with "/"`},
		{"/a//b", "empty path segment"},
		{"/../x", `".." segments`},
		{"/healthz?probe=1", `contains "?"`},
		{"/health{z", `contains "{"`},
		{"/health z", `contains " "`},
		{"/health*", `contains "*"`},
		{"/health#z", `contains "#"`},
	}
	for _, c := range cases {
		err := CheckExact(c.path)
		if err == nil {
			t.Errorf("CheckExact(%q): expected error, got none", c.path)
			continue
		}
		if !strings.Contains(err.Error(), c.errPart) {
			t.Errorf("CheckExact(%q) error = %q, want it to contain %q", c.path, err, c.errPart)
		}
	}
}

func TestOverlaps(t *testing.T) {
	cases := []struct {
		a, b    string
		overlap bool
	}{
		// literal vs literal
		{"/v1/account", "/v1/account", true},
		{"/v1/account", "/v1/billing", false},
		{"/v1/account", "/v1/account/x", false}, // depth mismatch, no tail
		// wildcard segments
		{"/v*/account", "/v1/account", true},
		{"/v*/account", "/w1/account", false},
		{"/v*", "/*1", true},  // witness: v1
		{"/a*", "/b*", false}, // first char can't agree
		{"/v*x", "/vax", true},
		{"/v*x", "/vx", false}, // * needs >=1 char
		// tails
		{"/tenants/**", "/tenants/v1/account", true},
		{"/tenants/**", "/tenants", true},
		{"/tenants/**", "/billing/v1", false},
		{"/a/**", "/a/b/**", true},
		{"/v*/account/**", "/v1/account/emails", true},
		{"/v*/account/**", "/v1/billing/emails", false},
		// mixed depth without tail never overlaps
		{"/a/b", "/a", false},
	}
	for _, c := range cases {
		pa, err := Parse(c.a)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.a, err)
		}
		pb, err := Parse(c.b)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.b, err)
		}
		if got := pa.Overlaps(pb); got != c.overlap {
			t.Errorf("Overlaps(%q, %q) = %v, want %v", c.a, c.b, got, c.overlap)
		}
		if got := pb.Overlaps(pa); got != c.overlap {
			t.Errorf("Overlaps(%q, %q) = %v, want %v (symmetry)", c.b, c.a, got, c.overlap)
		}
	}
}
