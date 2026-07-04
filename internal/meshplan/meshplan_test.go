package meshplan

import (
	"reflect"
	"testing"

	"github.com/wardnet/inforge/internal/types"
)

func TestServicesByHost(t *testing.T) {
	res := types.Resources{
		Service: []types.ServiceSpec{
			{Name: "zeta", Host: "bridge", Pki: "mesh"},
			{Name: "alpha", Host: "bridge", Pki: "mesh"},
			{Name: "plain", Host: "bridge"}, // no pki: — not a mesh member
			{Name: "solo", Host: "edge", Pki: "mesh"},
			{Name: "orphan", Host: "missing", Pki: "mesh"}, // unresolvable host — skipped
		},
	}
	canonical := map[string]string{"bridge": "bridge-01", "edge": "edge-01"}

	byHost := ServicesByHost(res, canonical)

	if got := HostKeys(byHost); !reflect.DeepEqual(got, []string{"bridge-01", "edge-01"}) {
		t.Fatalf("HostKeys = %v", got)
	}
	var names []string
	for _, svc := range byHost["bridge-01"] {
		names = append(names, svc.Name)
	}
	if !reflect.DeepEqual(names, []string{"alpha", "zeta"}) {
		t.Fatalf("bridge-01 services = %v, want sorted [alpha zeta]", names)
	}
	if len(byHost["edge-01"]) != 1 || byHost["edge-01"][0].Name != "solo" {
		t.Fatalf("edge-01 services = %v", byHost["edge-01"])
	}
}

func TestServicesByHostEmpty(t *testing.T) {
	if got := ServicesByHost(types.Resources{}, nil); len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}
