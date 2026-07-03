package program

import (
	"reflect"
	"testing"
)

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
