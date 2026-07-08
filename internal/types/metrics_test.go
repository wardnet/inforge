package types

import "testing"

func TestDatabaseMetricsEnabled(t *testing.T) {
	f := false
	tr := true
	cases := []struct {
		name string
		spec DatabaseSpec
		want bool
	}{
		{"default (omitted) is enabled", DatabaseSpec{}, true},
		{"metrics: true is enabled", DatabaseSpec{Metrics: &tr}, true},
		{"metrics: false opts out", DatabaseSpec{Metrics: &f}, false},
	}
	for _, c := range cases {
		if got := c.spec.MetricsEnabled(); got != c.want {
			t.Errorf("%s: MetricsEnabled() = %v, want %v", c.name, got, c.want)
		}
	}
}
