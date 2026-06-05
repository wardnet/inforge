package naming

import "testing"

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
