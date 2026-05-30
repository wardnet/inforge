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

func TestDisplayName(t *testing.T) {
	got := DisplayName("prd", "compute", "use1", "bridge", 1)
	want := "wardnet-prd-compute-use1-bridge-01"
	if got != want {
		t.Errorf("DisplayName = %q, want %q", got, want)
	}
}
