package program

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/types"
)

// recordingProvisioner records the role names and permissions it is asked to mint.
type recordingProvisioner struct{ calls []string }

func (p *recordingProvisioner) ProvisionRole(_ *pulumi.Context, roleName, permission string) (types.DBRoleFields, error) {
	p.calls = append(p.calls, roleName+"|"+permission)
	return types.DBRoleFields{
		User:     pulumi.String("u").ToStringOutput(),
		Password: pulumi.String("p").ToStringOutput(),
		Host:     pulumi.String("h").ToStringOutput(),
		Port:     pulumi.String("5432").ToStringOutput(),
		DBName:   pulumi.String("db").ToStringOutput(),
	}, nil
}

// TestResolveDatabaseGrants covers the cross-region uniform path: a regional
// service's database/* grant resolves against its own region, a database/global/*
// grant redirects to the global slot (the same redirect ref: uses), each role is
// named for the CONSUMING service instance (consumer env+slug), and pki/* grants
// are not materialized here (slice C).
func TestResolveDatabaseGrants(t *testing.T) {
	regional := &recordingProvisioner{}
	global := &recordingProvisioner{}
	all := types.AllOutputs{Database: map[string]map[string]types.DatabaseOutputs{
		"us-east-1": {"main": {RoleProvisioner: regional}},
		"global":    {"shared": {RoleProvisioner: global}},
	}}
	svc := types.ServiceSpec{Name: "api", Grants: []types.GrantSpec{
		{Resource: "database/main", Permission: "rw", Outputs: map[string]string{"DB_URL": "{USER}@{HOST}"}},
		{Resource: "database/global/shared", Permission: "ro", Outputs: map[string]string{"SHARED_URL": "{USER}@{HOST}"}},
		{Resource: "pki/daemon", Permission: "ro", Outputs: map[string]string{"CA": "{CERT}"}},
	}}

	out, err := resolveDatabaseGrants(nil, svc, all, "prd", "us-east-1", "use1")
	require.NoError(t, err)

	assert.Contains(t, out, "DB_URL")
	assert.Contains(t, out, "SHARED_URL")
	assert.NotContains(t, out, "CA", "pki/* grants are materialized in slice C, not here")

	// Consumer-scoped role names (consumer env+slug), regional vs global slot.
	require.Equal(t, []string{"wardnet-prd-use1-dbrole-api-main|rw"}, regional.calls)
	require.Equal(t, []string{"wardnet-prd-use1-dbrole-api-shared|ro"}, global.calls,
		"a global database grant uses the global slot but the CONSUMER's slug for the role name")
}

func TestResolveDatabaseGrantsNoGrants(t *testing.T) {
	out, err := resolveDatabaseGrants(nil, types.ServiceSpec{Name: "api"}, types.AllOutputs{}, "prd", "us-east-1", "use1")
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestResolveDatabaseGrantsMissingTarget(t *testing.T) {
	svc := types.ServiceSpec{Name: "api", Grants: []types.GrantSpec{
		{Resource: "database/nope", Permission: "ro", Outputs: map[string]string{"X": "{USER}"}},
	}}
	_, err := resolveDatabaseGrants(nil, svc, types.AllOutputs{Database: map[string]map[string]types.DatabaseOutputs{"us-east-1": {}}}, "prd", "us-east-1", "use1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
