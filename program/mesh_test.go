package program

import (
	"reflect"
	"testing"

	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/types"
)

func TestMeshInputsByHost(t *testing.T) {
	res := types.Resources{Service: []types.ServiceSpec{
		{Name: "tunneller", Host: "bridge", Pki: "m", Mesh: &types.MeshSpec{Port: 9090, AllowedServices: []string{"ddns"}}},
		{Name: "ddns", Host: "bridge", Pki: "m", Mesh: &types.MeshSpec{Port: 8080, AllowedServices: []string{"tunneller", "gateway"}}},
		{Name: "probe", Host: "bridge", Pki: "m"}, // pki but no mesh block -> egress-only caller
		{Name: "nomesh", Host: "bridge"},          // no pki -> skipped entirely
	}}
	canonical := map[string]string{"bridge": "bridge-01"}
	allowedFor := func(svc types.ServiceSpec) []string {
		if svc.Mesh == nil {
			return nil
		}
		return svc.Mesh.AllowedServices // passthrough for the test
	}
	mh := meshInputsByHost(res, canonical, "us-east-1", allowedFor)["bridge-01"]
	if mh == nil {
		t.Fatal("no inputs for bridge-01")
	}
	// Egress: the 3 pki services, sorted by name (ddns, probe, tunneller), ports 9500..9502.
	if len(mh.egress) != 3 {
		t.Fatalf("egress count = %d, want 3", len(mh.egress))
	}
	if mh.egress[0].Name != "ddns" || mh.egress[0].EgressPort != meshpaths.EgressBase {
		t.Errorf("egress[0] = %+v", mh.egress[0])
	}
	if mh.egress[2].Name != "tunneller" || mh.egress[2].EgressPort != meshpaths.EgressBase+2 {
		t.Errorf("egress[2] = %+v", mh.egress[2])
	}
	// Local: only the 2 with a mesh block (ddns, tunneller).
	if len(mh.local) != 2 {
		t.Fatalf("local count = %d, want 2", len(mh.local))
	}
	if mh.local[0].Name != "ddns" || mh.local[0].SNI != "ddns.us-east-1.mesh" || mh.local[0].MeshPort != 8080 {
		t.Errorf("local[0] = %+v", mh.local[0])
	}
	if !reflect.DeepEqual(mh.local[0].AllowedCallers, []string{"tunneller", "gateway"}) {
		t.Errorf("local[0].AllowedCallers = %v", mh.local[0].AllowedCallers)
	}
	if mh.local[0].LeafCertPath != meshpaths.LeafCertPath("ddns") {
		t.Errorf("local[0].LeafCertPath = %q", mh.local[0].LeafCertPath)
	}
}

func TestMeshEgressPortsByService(t *testing.T) {
	res := types.Resources{Service: []types.ServiceSpec{
		{Name: "tunneller", Host: "bridge", Pki: "m"},
		{Name: "ddns", Host: "bridge", Pki: "m"},
		{Name: "probe", Host: "worker", Pki: "m"}, // different host -> its own base port
		{Name: "nomesh", Host: "bridge"},          // no pki -> no egress port
	}}
	canonical := map[string]string{"bridge": "bridge-01", "worker": "worker-01"}
	got := meshEgressPortsByService(res, canonical)
	// bridge's services sort ddns(9500), tunneller(9501); worker's probe restarts at 9500.
	if got["ddns"] != meshpaths.EgressBase || got["tunneller"] != meshpaths.EgressBase+1 {
		t.Errorf("bridge ports = ddns:%d tunneller:%d", got["ddns"], got["tunneller"])
	}
	if got["probe"] != meshpaths.EgressBase {
		t.Errorf("worker probe port = %d, want %d", got["probe"], meshpaths.EgressBase)
	}
	if _, ok := got["nomesh"]; ok {
		t.Error("a non-pki service must get no egress port")
	}
}

func TestMeshTargetsRegionalIncludesGlobal(t *testing.T) {
	scopeRes := types.Resources{
		Compute: []types.ComputeSpec{{Name: "bridge", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "ddns", Host: "bridge", Pki: "m", Mesh: &types.MeshSpec{Port: 8080}},   // callee -> target
			{Name: "caller", Host: "bridge", Pki: "m"},                                     // egress-only -> NOT a target
		},
	}
	globalRes := types.Resources{
		Compute: []types.ComputeSpec{{Name: "core", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "tenants", Host: "core", Pki: "m", Mesh: &types.MeshSpec{Port: 9090}}, // global callee -> cross-scope target
		},
	}
	sc := naming.CanonicalComputeKeys(scopeRes.Compute)
	gc := naming.CanonicalComputeKeys(globalRes.Compute)

	got := meshTargets(scopeRes, globalRes, sc, gc, "us-east-1", false)
	// ddns (same-scope, private, SNI ddns.us-east-1.mesh) + tenants (global, public, SNI tenants.global.mesh).
	if len(got) != 2 {
		t.Fatalf("targets = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].name != "ddns" || got[0].global || got[0].sni != "ddns.us-east-1.mesh" || got[0].host != "bridge-01" {
		t.Errorf("target[0] = %+v", got[0])
	}
	if got[1].name != "tenants" || !got[1].global || got[1].sni != "tenants.global.mesh" || got[1].host != "core-01" {
		t.Errorf("target[1] = %+v", got[1])
	}
}

func TestMeshTargetsGlobalScopeHasNoCrossScope(t *testing.T) {
	scopeRes := types.Resources{
		Compute: []types.ComputeSpec{{Name: "core", InstanceCount: 1}},
		Service: []types.ServiceSpec{
			{Name: "tenants", Host: "core", Pki: "m", Mesh: &types.MeshSpec{Port: 9090}},
		},
	}
	// The global scope passes itself as globalRes; isGlobal=true drops the cross-scope pass.
	got := meshTargets(scopeRes, scopeRes, naming.CanonicalComputeKeys(scopeRes.Compute), naming.CanonicalComputeKeys(scopeRes.Compute), "global", true)
	if len(got) != 1 || got[0].name != "tenants" || got[0].global {
		t.Fatalf("global-scope targets = %+v, want one same-scope tenants", got)
	}
}

func TestExpandAllowedCallersRegionalCallee(t *testing.T) {
	regions := []string{"us-east-1", "eu-central-1"}
	regionalMesh := map[string]bool{"ddns": true, "tunneller": true}
	globalMesh := map[string]bool{"tenants": true}

	// A regional callee (rendered for region us-east-1) admits same-region callers and
	// its own region's gateway — never another region, never global.
	got := expandAllowedCallers([]string{"gateway", "ddns"}, "us-east-1", regions, regionalMesh, globalMesh)
	want := []string{"us-east-1/ddns", "us-east-1/gateway"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("regional callee = %v, want %v", got, want)
	}
}

func TestExpandAllowedCallersGlobalCalleeGateway(t *testing.T) {
	// A global callee is only called by the global gateway (the gateway routes same-scope
	// only), never a regional gateway.
	got := expandAllowedCallers([]string{"gateway"}, "global", []string{"us-east-1"}, nil, nil)
	want := []string{"global/gateway"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("global callee gateway = %v, want %v", got, want)
	}
}

func TestExpandAllowedCallersGlobalCalleeRegionalCaller(t *testing.T) {
	regions := []string{"us-east-1", "eu-central-1"}
	regionalMesh := map[string]bool{"ddns": true}
	// A regional caller may reach a global callee from EVERY region (regional→global).
	got := expandAllowedCallers([]string{"ddns"}, "global", regions, regionalMesh, nil)
	want := []string{"eu-central-1/ddns", "us-east-1/ddns"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("global callee regional caller = %v, want %v", got, want)
	}
}

func TestExpandAllowedCallersGlobalCalleeGlobalCaller(t *testing.T) {
	globalMesh := map[string]bool{"billing": true}
	got := expandAllowedCallers([]string{"billing"}, "global", []string{"us-east-1"}, nil, globalMesh)
	want := []string{"global/billing"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("global callee global caller = %v, want %v", got, want)
	}
}

func TestExpandAllowedCallersNameInBothSets(t *testing.T) {
	regions := []string{"us-east-1"}
	// A name that is both a regional and a global mesh service expands to both.
	got := expandAllowedCallers([]string{"both"}, "global", regions,
		map[string]bool{"both": true}, map[string]bool{"both": true})
	want := []string{"global/both", "us-east-1/both"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("name in both sets = %v, want %v", got, want)
	}
}

func TestExpandAllowedCallersDedupAndEmpty(t *testing.T) {
	if got := expandAllowedCallers(nil, "us-east-1", nil, nil, nil); len(got) != 0 {
		t.Errorf("empty allow list = %v, want none", got)
	}
	got := expandAllowedCallers([]string{"ddns", "ddns"}, "us-east-1", []string{"us-east-1"}, map[string]bool{"ddns": true}, nil)
	if !reflect.DeepEqual(got, []string{"us-east-1/ddns"}) {
		t.Errorf("dedup = %v, want [us-east-1/ddns]", got)
	}
}
