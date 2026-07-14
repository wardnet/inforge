package naming

import (
	"testing"

	"github.com/wardnet/inforge/internal/types"
)

func TestCanonicalComputeKeys(t *testing.T) {
	canonical := CanonicalComputeKeys([]types.ComputeSpec{
		{Name: "bridge", InstanceCount: 1},
		{Name: "worker", InstanceCount: 2},
	})

	// Single-instance: both the bare name and the expanded key resolve.
	if got := canonical["bridge"]; got != "bridge-01" {
		t.Errorf(`canonical["bridge"] = %q, want "bridge-01"`, got)
	}
	if got := canonical["bridge-01"]; got != "bridge-01" {
		t.Errorf(`canonical["bridge-01"] = %q, want "bridge-01"`, got)
	}

	// Multi-instance: each expanded key resolves to itself; the bare name does
	// NOT (it would be ambiguous between instances).
	if got := canonical["worker-02"]; got != "worker-02" {
		t.Errorf(`canonical["worker-02"] = %q, want "worker-02"`, got)
	}
	if _, ok := canonical["worker"]; ok {
		t.Error(`canonical["worker"] should not resolve for a multi-instance compute`)
	}
}

func TestSpecKey(t *testing.T) {
	cases := []struct {
		name     string
		instance int
		want     string
	}{
		{"bridge", 1, "bridge-01"},
		{"bridge", 12, "bridge-12"},
		{"ingress", 9, "ingress-09"},
		{"db", 100, "db-100"},
	}
	for _, c := range cases {
		if got := SpecKey(c.name, c.instance); got != c.want {
			t.Errorf("SpecKey(%q, %d) = %q, want %q", c.name, c.instance, got, c.want)
		}
	}
}

func TestResource(t *testing.T) {
	got := Resource("prd", "use1", "vm", "bridge")
	want := "wardnet-prd-use1-vm-bridge"
	if got != want {
		t.Errorf("Resource = %q, want %q", got, want)
	}
}

func TestResourceInstance(t *testing.T) {
	got := ResourceInstance("prd", "use1", "vm", "bridge", 1)
	want := "wardnet-prd-use1-vm-bridge-01"
	if got != want {
		t.Errorf("ResourceInstance = %q, want %q", got, want)
	}
}

// TestResourceGlobalScope: an empty slug drops the region segment, yielding the
// region-less global name (identical to GlobalResource).
func TestResourceGlobalScope(t *testing.T) {
	if got, want := Resource("prd", "", "db", "main"), "wardnet-prd-db-main"; got != want {
		t.Errorf("Resource(global) = %q, want %q", got, want)
	}
	if got, want := Resource("prd", "", "db", "main"), GlobalResource("prd", "db", "main"); got != want {
		t.Errorf("Resource with empty slug = %q, want GlobalResource %q", got, want)
	}
}

// TestResourceInstanceGlobalScope: an empty slug drops the region segment but
// keeps the -NN instance suffix.
func TestResourceInstanceGlobalScope(t *testing.T) {
	if got, want := ResourceInstance("prd", "", "vm", "bridge", 1), "wardnet-prd-vm-bridge-01"; got != want {
		t.Errorf("ResourceInstance(global) = %q, want %q", got, want)
	}
}

// TestRecordNameGlobalScope: an empty slug drops the region segment.
func TestRecordNameGlobalScope(t *testing.T) {
	if got, want := RecordName("prd", "", "bridge"), "bridge.prd"; got != want {
		t.Errorf("RecordName(global) = %q, want %q", got, want)
	}
}

func TestGlobalResource(t *testing.T) {
	got := GlobalResource("prd", "key", "user")
	want := "wardnet-prd-key-user"
	if got != want {
		t.Errorf("GlobalResource = %q, want %q", got, want)
	}
}

func TestRecordName(t *testing.T) {
	got := RecordName("prd", "use1", "bridge")
	want := "bridge.prd.use1"
	if got != want {
		t.Errorf("RecordName = %q, want %q", got, want)
	}
}

func TestRecordFQDN(t *testing.T) {
	got := RecordFQDN("prd", "use1", "bridge", "wardnet.network")
	want := "bridge.prd.use1.wardnet.network"
	if got != want {
		t.Errorf("RecordFQDN = %q, want %q", got, want)
	}
}

func TestHostFQDN(t *testing.T) {
	got := HostFQDN("prd", "use1", "bridge", "wardnet.network")
	want := "bridge.vm.prd.use1.wardnet.network"
	if got != want {
		t.Errorf("HostFQDN = %q, want %q", got, want)
	}
}

func TestServiceFQDN(t *testing.T) {
	got := ServiceFQDN("prd", "use1", "bridge", "wardnet.network")
	want := "bridge.svc.prd.use1.wardnet.network"
	if got != want {
		t.Errorf("ServiceFQDN = %q, want %q", got, want)
	}
}

func TestAppFQDN(t *testing.T) {
	// Regional: the slug segment is present, but NO env segment (unlike
	// RecordFQDN/ServiceFQDN) — an app uses a per-env base domain.
	if got, want := AppFQDN("my", "use1", "wardnet.network", ""), "my.use1.wardnet.network"; got != want {
		t.Errorf("AppFQDN regional = %q, want %q", got, want)
	}
	// Global: an empty slug drops the region segment entirely.
	if got, want := AppFQDN("marketing", "", "wardnet.network", ""), "marketing.wardnet.network"; got != want {
		t.Errorf("AppFQDN global = %q, want %q", got, want)
	}
	// Ephemeral (ADR-0028): the slug identity segment is inserted right after the
	// subdomain so the clone never collides with the source's app hostname.
	if got, want := AppFQDN("my", "use1", "wardnet.network", "eph-7f3k"), "my.eph-7f3k.use1.wardnet.network"; got != want {
		t.Errorf("AppFQDN ephemeral regional = %q, want %q", got, want)
	}
	if got, want := AppFQDN("marketing", "", "wardnet.network", "eph-7f3k"), "marketing.eph-7f3k.wardnet.network"; got != want {
		t.Errorf("AppFQDN ephemeral global = %q, want %q", got, want)
	}
}

func TestExpandVanity(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"bare token is env+region scoped", "foo", "foo.prd.use1.wardnet.network"},
		{"base-domain placeholder", "key-broker.{BASE_DOMAIN}", "key-broker.wardnet.network"},
		{"apex via base-domain placeholder", "{BASE_DOMAIN}", "wardnet.network"},
		{"literal full fqdn used as-is", "key-broker.inforge.wardnet.network", "key-broker.inforge.wardnet.network"},
		{"env and slug placeholders", "x.{ENV}.{REGION_SLUG}.{BASE_DOMAIN}", "x.prd.use1.wardnet.network"},
		// Degenerate: a lone placeholder contains "{", so it takes the template
		// branch and is substituted verbatim — NOT env/region-scoped. Documented,
		// not a bug: such a vanity is operator nonsense, but the result is
		// deterministic rather than surprising.
		{"lone ENV placeholder is not scoped", "{ENV}", "prd"},
		{"lone REGION_SLUG placeholder is not scoped", "{REGION_SLUG}", "use1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExpandVanity(c.in, "prd", "use1", "wardnet.network"); got != c.want {
				t.Errorf("ExpandVanity(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestZoneRelative(t *testing.T) {
	cases := []struct{ name, fqdn, want string }{
		{"normal", "bridge.svc.prd.use1.wardnet.network", "bridge.svc.prd.use1"},
		{"multi-label vanity", "key-broker.inforge.wardnet.network", "key-broker.inforge"},
		{"single label", "key-broker.wardnet.network", "key-broker"},
		{"apex", "wardnet.network", "@"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ZoneRelative(c.fqdn, "wardnet.network"); got != c.want {
				t.Errorf("ZoneRelative(%q) = %q, want %q", c.fqdn, got, c.want)
			}
		})
	}
}

// InZone is ZoneRelative's precondition: an out-of-zone FQDN survives ZoneRelative
// unchanged and would be created as "<fqdn>.<baseDomain>" inside the authority's
// zone, so callers taking an authored FQDN (a route vanity) gate on it. The
// look-alike suffix case is the one a naive HasSuffix without the dot would miss.
func TestInZone(t *testing.T) {
	cases := []struct {
		fqdn string
		want bool
	}{
		{"bridge.svc.prd.use1.wardnet.network", true},
		{"account.wardnet.network", true},
		{"wardnet.network", true}, // the apex
		{"shop.other.net", false},
		{"shop.notwardnet.network", false}, // look-alike suffix, not a subdomain
		{"wardnet.network.evil.com", false},
	}
	for _, c := range cases {
		t.Run(c.fqdn, func(t *testing.T) {
			if got := InZone(c.fqdn, "wardnet.network"); got != c.want {
				t.Errorf("InZone(%q) = %v, want %v", c.fqdn, got, c.want)
			}
		})
	}
}

func TestParseResourceName(t *testing.T) {
	cases := []struct {
		name            string
		typ, rest, slug string
		ok              bool
	}{
		{"wardnet-prd-use1-svc-tenants-provision", "svc", "tenants-provision", "use1", true},
		{"wardnet-prd-use1-vol-pg", "vol", "pg", "use1", true},
		// env-global names carry no slug segment
		{"wardnet-prd-key-user", "key", "user", "", true},
		// an ephemeral env's slug is hyphenated ("eph-<rand>") — the type token
		// is found by scan, not position
		{"wardnet-eph-x1y2-use1-dbrole-tenants-tenants-mint", "dbrole", "tenants-tenants-mint", "use1", true},
		// outside the grammar
		{"tenants-pw", "", "", "", false},
		{"wardnet-prd-nope", "", "", "", false},
	}
	for _, c := range cases {
		typ, rest, slug, ok := ParseResourceName(c.name)
		if typ != c.typ || rest != c.rest || slug != c.slug || ok != c.ok {
			t.Errorf("ParseResourceName(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.name, typ, rest, slug, ok, c.typ, c.rest, c.slug, c.ok)
		}
	}
}
