package validate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/meshcert"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/types"
)

// grantCtx builds a regionContext with a regional database "main", a global
// database "shared", a root-only PKI resource "daemon", a global root-only PKI
// "rootca", and a two-tier-by-mistake PKI resource "bad". Every declared
// root-only PKI is also GENERATED (has material in the store) — pkiGenerated is
// keyed by bare name, so the "global/" grant form resolves to "rootca".
func grantCtx() regionContext {
	return regionContext{
		databaseNames: map[string]bool{"main": true, "global/shared": true},
		pkiResources: map[string]string{
			"daemon":        pki.TopologyRootOnly,
			"global/rootca": pki.TopologyRootOnly,
			"bad":           pki.TopologyTwoTier,
			// Declared in a manifest but never generated into the store.
			"ungenerated": pki.TopologyRootOnly,
		},
		pkiGenerated: map[string]bool{"daemon": true, "rootca": true},
	}
}

// grantSvc builds a service carrying the given grants and environment keys.
func grantSvc(env map[string]string, grants ...types.GrantSpec) types.ServiceSpec {
	return types.ServiceSpec{Name: "api", Container: "bridge", Host: "bridge", Type: "raw", User: "api", Pki: "wardnet-mesh", Grants: grants, Environment: env}
}

func TestCheckGrantsValid(t *testing.T) {
	ctx := grantCtx()
	tests := []struct {
		name  string
		grant types.GrantSpec
	}{
		{"database rw composed url", types.GrantSpec{Resource: "database/main", Permission: "rw", Outputs: map[string]string{"DB_URL": "{USER}:{PASSWORD}@{HOST}:{PORT}/{DBNAME}"}}},
		{"database ro discrete vars", types.GrantSpec{Resource: "database/main", Permission: "ro", Outputs: map[string]string{"DB_USER": "{USER}", "DB_PASS": "{PASSWORD}"}}},
		{"pki verify cert", types.GrantSpec{Resource: "pki/daemon", Permission: "ro", Outputs: map[string]string{"CA_CERT_PATH": "{CERT}"}}},
		{"pki issue cert+key", types.GrantSpec{Resource: "pki/daemon", Permission: "rw", Outputs: map[string]string{"CA_CERT_PATH": "{CERT}", "CA_KEY_PATH": "{KEY}"}}},
		{"pki global target", types.GrantSpec{Resource: "pki/global/rootca", Permission: "ro", Outputs: map[string]string{"CA_CERT_PATH": "{CERT}"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Empty(t, checkGrants(grantSvc(nil, tc.grant), ctx))
		})
	}
}

// TestCheckGrantsDatabaseSameNetwork: a database grant whose cluster host is on a
// different network than the granting service's host is rejected (the cluster's
// 5432 opens to its own network CIDR only); same-network passes.
func TestCheckGrantsDatabaseSameNetwork(t *testing.T) {
	base := func() regionContext {
		return regionContext{
			databaseNames:         map[string]bool{"main": true},
			databaseClusterByName: map[string]string{"main": "pg"},
			clusterHostByName:     map[string]string{"pg": "db-01"},
			computeNames:          map[string]bool{"bridge": true, "db": true},
			computeCanonical:      map[string]string{"bridge": "bridge-01", "db": "db-01"},
			computeNetwork:        map[string]string{"bridge-01": "net-a", "db-01": "net-b"},
		}
	}
	grant := types.GrantSpec{Resource: "database/main", Permission: "ro", Outputs: map[string]string{"DB_URL": "{URL}"}}

	// Different networks: rejected.
	errs := checkGrants(grantSvc(nil, grant), base())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must share a network")

	// Same network: allowed.
	ctx := base()
	ctx.computeNetwork["db-01"] = "net-a"
	assert.Empty(t, checkGrants(grantSvc(nil, grant), ctx))
}

func TestCheckGrantsErrors(t *testing.T) {
	ctx := grantCtx()
	tests := []struct {
		name    string
		env     map[string]string
		grants  []types.GrantSpec
		wantSub string
	}{
		{"malformed resource", nil, []types.GrantSpec{{Resource: "database", Permission: "ro", Outputs: map[string]string{"X": "{USER}"}}}, "must be \"<type>/<name>\""},
		{"unsupported type", nil, []types.GrantSpec{{Resource: "kafka/events", Permission: "ro", Outputs: map[string]string{"X": "{USER}"}}}, "not a grantable type"},
		{"invalid permission", nil, []types.GrantSpec{{Resource: "database/main", Permission: "admin", Outputs: map[string]string{"X": "{USER}"}}}, "permission"},
		{"database not found", nil, []types.GrantSpec{{Resource: "database/nope", Permission: "ro", Outputs: map[string]string{"X": "{USER}"}}}, "database \"nope\" not found"},
		{"regional cannot reach global-less name", nil, []types.GrantSpec{{Resource: "database/shared", Permission: "ro", Outputs: map[string]string{"X": "{USER}"}}}, "not found"},
		{"cross-scope database grant rejected", nil, []types.GrantSpec{{Resource: "database/global/shared", Permission: "ro", Outputs: map[string]string{"X": "{USER}"}}}, "cross-scope database access is not supported"},
		{"pki not found", nil, []types.GrantSpec{{Resource: "pki/nope", Permission: "ro", Outputs: map[string]string{"X": "{CERT}"}}}, "pki resource \"nope\" not found"},
		// A PKI resource may be declared in a manifest yet never generated into the
		// store. The grant then has nothing to deliver: the deploy would write a
		// descriptor without the grant's env vars and the service would crash-loop on
		// a required variable. Fail at validate instead.
		{"pki declared but never generated", nil, []types.GrantSpec{{Resource: "pki/ungenerated", Permission: "rw", Outputs: map[string]string{"X": "{KEY}"}}}, "has no material in"},
		{"pki not root-only", nil, []types.GrantSpec{{Resource: "pki/bad", Permission: "ro", Outputs: map[string]string{"X": "{CERT}"}}}, "must be \"root-only\""},
		{"unpublished value field", nil, []types.GrantSpec{{Resource: "database/main", Permission: "ro", Outputs: map[string]string{"X": "{FOO}"}}}, "field {FOO} is not published"},
		{"key not published for verify", nil, []types.GrantSpec{{Resource: "pki/daemon", Permission: "ro", Outputs: map[string]string{"X": "{KEY}"}}}, "field {KEY} is not published"},
		{"file field with literal", nil, []types.GrantSpec{{Resource: "pki/daemon", Permission: "ro", Outputs: map[string]string{"X": "prefix-{CERT}"}}}, "must stand alone"},
		{"file field with another token", nil, []types.GrantSpec{{Resource: "pki/daemon", Permission: "rw", Outputs: map[string]string{"X": "{CERT}{KEY}"}}}, "must stand alone"},
		{"reserved INFORGE prefix", nil, []types.GrantSpec{{Resource: "database/main", Permission: "ro", Outputs: map[string]string{"INFORGE_DB": "{USER}"}}}, "reserved INFORGE_*"},
		{"reserved mesh path name", nil, []types.GrantSpec{{Resource: "pki/daemon", Permission: "ro", Outputs: map[string]string{meshcert.EnvLeafCertPath: "{CERT}"}}}, "reserved by inforge for mesh certificate paths"},
		{"collides with environment key", map[string]string{"DB_URL": "literal"}, []types.GrantSpec{{Resource: "database/main", Permission: "ro", Outputs: map[string]string{"DB_URL": "{USER}"}}}, "collides with a key in the service's environment.yaml"},
		{"collides across grants", nil, []types.GrantSpec{
			{Resource: "database/main", Permission: "ro", Outputs: map[string]string{"SHARED": "{USER}"}},
			{Resource: "pki/daemon", Permission: "ro", Outputs: map[string]string{"SHARED": "{CERT}"}},
		}, "also produced by the grant"},
		{"template parse error", nil, []types.GrantSpec{{Resource: "database/main", Permission: "ro", Outputs: map[string]string{"X": "{USER"}}}, "unbalanced"},
		{"literal-only template (dropped braces)", nil, []types.GrantSpec{{Resource: "database/main", Permission: "ro", Outputs: map[string]string{"X": "static"}}}, "interpolates no field"},
		{"whitespace-only template", nil, []types.GrantSpec{{Resource: "database/main", Permission: "ro", Outputs: map[string]string{"X": " "}}}, "interpolates no field"},
		{"duplicate grant target", nil, []types.GrantSpec{
			{Resource: "database/main", Permission: "ro", Outputs: map[string]string{"RO_URL": "{URL}"}},
			{Resource: "database/main", Permission: "rw", Outputs: map[string]string{"RW_URL": "{URL}"}},
		}, "granted more than once"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := checkGrants(grantSvc(tc.env, tc.grants...), ctx)
			require.NotEmpty(t, errs)
			assert.True(t, containsSub(errs, tc.wantSub), "errors %v do not contain %q", errs, tc.wantSub)
		})
	}
}

// TestCheckGrantsUnresolvedTargetNoCascade: a grant whose target does not resolve
// reports only the not-found error, not a misleading "field not published" cascade
// from validating outputs against a zero-value Grantable.
func TestCheckGrantsUnresolvedTargetNoCascade(t *testing.T) {
	ctx := grantCtx()

	errs := checkGrants(grantSvc(nil, types.GrantSpec{Resource: "database/nope", Permission: "ro", Outputs: map[string]string{"X": "{BOGUS}"}}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "database \"nope\" not found")
	assert.False(t, containsSub(errs, "not published"), "must not cascade field-publication errors for an unresolved target")

	// A non-root-only pki target is also "unresolved" for field checks.
	errs = checkGrants(grantSvc(nil, types.GrantSpec{Resource: "pki/bad", Permission: "rw", Outputs: map[string]string{"X": "{BOGUS}"}}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "must be \"root-only\"")
	assert.False(t, containsSub(errs, "not published"))
}

// TestGlobalHasResourcesCountsPKI: a global slice declaring only a PKI resource
// must still count as non-empty, so the "global resources need a providers block"
// guard fires (the PKI cert material is written to that block's secrets provider).
func TestGlobalHasResourcesCountsPKI(t *testing.T) {
	assert.False(t, types.Resources{}.HasAny())
	assert.True(t, types.Resources{PKI: []types.PKIResourceSpec{{Name: "rootca"}}}.HasAny(),
		"a global slice with only a PKI resource must count as having resources")
}

func TestCheckPKIResource(t *testing.T) {
	assert.Empty(t, errsOf(checkPKIResource(types.PKIResourceSpec{Name: "daemon", Container: "bridge", Topology: pki.TopologyRootOnly})))

	errs := errsOf(checkPKIResource(types.PKIResourceSpec{Name: "daemon", Container: "bridge", Topology: pki.TopologyTwoTier}))
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "must be \"root-only\"")
}

func containsSub(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func errsOf(errs, _ []string) []string { return errs }
