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
