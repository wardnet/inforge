package program

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/postgres"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/types"
)

// obsResources is one host running one Postgres cluster with one scraped database —
// the smallest stack that emits both an observability monitor-role mint and a
// collector config command.
func obsResources() (types.Resources, map[string]types.ComputeOutputs) {
	res := types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "edge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
		},
		DatabaseCluster: []types.DatabaseClusterSpec{
			{Name: "pg", Host: "edge", Provider: string(types.SelfHostedProvider)},
		},
		Database: []types.DatabaseSpec{
			{Name: "app", Cluster: "pg", Database: "appdb", Owner: "appowner", Backup: types.BackupPolicy{Interval: "24h", Keep: 7}},
		},
	}
	computeOut := map[string]types.ComputeOutputs{
		"edge-01": {
			PublicIP:  pulumi.String("1.2.3.4").ToStringOutput(),
			PrivateIP: pulumi.String("10.0.0.2").ToStringOutput(),
		},
	}
	return res, computeOut
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestObservabilityTriggersAreNotSecret: both observability commands that carry a
// secret script — the pg_monitor role mint (its RandomPassword) and the collector
// config write (the OTLP credential + every monitor password) — must put only a
// safeTrigger HASH in Triggers. A raw secret+unknown trigger aborts the whole preview
// with `malformed RPC secret: missing value for "triggers"` on a fresh cluster.
func TestObservabilityTriggersAreNotSecret(t *testing.T) {
	mocks := newCommandMocks()
	res, computeOut := obsResources()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		authB64 := pulumi.ToSecret(pulumi.String("dXNlcjpwdw==")).(pulumi.StringOutput)
		obs := types.ObservabilityConfig{OTLPEndpoint: "https://otlp.example.com/v1/metrics"}
		return provisionObservability(ctx, res, computeOut, nil, map[string]pulumi.Resource{}, nil, obs, authB64, "priv", "prd", "use1")
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	mint := mocks.captured[monitorRoleName("prd", "use1", "pg")+"-mint"]
	require.NotEmpty(t, mint.create, "the monitor-role mint must be registered")
	require.Len(t, mint.triggers, 1)
	assert.Equal(t, sha256Hex(mint.create), mint.triggers[0],
		"the mint script embeds the monitor password — Triggers must carry its safeTrigger hash, not the script")

	cfg := mocks.captured[naming.Resource("prd", "use1", "otelcol", "edge-01")+"-config"]
	require.NotEmpty(t, cfg.create, "the collector config command must be registered")
	require.Len(t, cfg.triggers, 1)
	assert.False(t, cfg.triggersSecret,
		"the config script carries the OTLP credential — a secret must never enter Triggers")
	assert.Equal(t, sha256Hex(cfg.create), cfg.triggers[0],
		"Triggers must carry the safeTrigger hash of the config script, not the script")
}

// TestMonitorMintWaitsOnServiceRoleMints: the observability monitor mint and the
// per-service grant mints on the same cluster both run CREATE ROLE / GRANT CONNECT ON
// DATABASE against catalogs shared by the whole cluster. Unserialized, two concurrent
// sessions updating one catalog tuple fail the deploy with "tuple concurrently
// updated" — so the monitor mint must depend on each database's newest role mint.
func TestMonitorMintWaitsOnServiceRoleMints(t *testing.T) {
	mocks := newCommandMocks()
	res, computeOut := obsResources()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		conn := iremote.Connection(pulumi.String("1.2.3.4"), "deploy", "priv")
		dbCmd, err := remote.NewCommand(ctx, "pg-db-app", &remote.CommandArgs{
			Connection: conn,
			Create:     pulumi.String("true"),
		})
		if err != nil {
			return err
		}
		p := &selfHostedRoleProvisioner{
			conn:      conn,
			privateIP: pulumi.String("10.0.0.2").ToStringOutput(),
			port:      postgres.ClusterPort(0),
			database:  "appdb",
			owner:     "appowner",
			dependsOn: dbCmd,
		}
		if _, err := p.ProvisionRole(ctx, "ghost-role", "rw"); err != nil {
			return err
		}
		dbOut := map[string]types.DatabaseOutputs{"app": {RoleProvisioner: p}}
		authB64 := pulumi.ToSecret(pulumi.String("dXNlcjpwdw==")).(pulumi.StringOutput)
		obs := types.ObservabilityConfig{OTLPEndpoint: "https://otlp.example.com/v1/metrics"}
		return provisionObservability(ctx, res, computeOut, dbOut, map[string]pulumi.Resource{}, nil, obs, authB64, "priv", "prd", "use1")
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	mint := monitorRoleName("prd", "use1", "pg") + "-mint"
	require.Contains(t, mocks.captured, mint)
	assert.True(t, mocks.dependsOn(mint, "ghost-role-mint"),
		"the monitor mint must wait on the cluster's per-service role mints — concurrent GRANTs race the shared catalog")
}

// TestCheckDBRoleNames: the derived role name dash-joins two names (not injective) and
// is handed to Postgres verbatim (truncated past 63 bytes). Both an ambiguous pair and
// an over-long name must fail the deploy with a rename hint instead of silently sharing
// one Postgres role — or, for a truncated name, minting a role the service cannot even
// log in as (the connection URL carries the untruncated name).
func TestCheckDBRoleNames(t *testing.T) {
	svc := func(name string, dbs ...string) types.ServiceSpec {
		s := types.ServiceSpec{Name: name}
		for _, db := range dbs {
			s.Grants = append(s.Grants, types.GrantSpec{Resource: "database/" + db, Permission: "rw"})
		}
		return s
	}
	tests := []struct {
		name    string
		res     types.Resources
		wantErr bool
	}{
		{
			name: "distinct pairs pass",
			res:  types.Resources{Service: []types.ServiceSpec{svc("tenants", "tenants"), svc("ddns", "ddns")}},
		},
		{
			name:    "dash-join collision across two services",
			res:     types.Resources{Service: []types.ServiceSpec{svc("api-eu", "ddns"), svc("api", "eu-ddns")}},
			wantErr: true,
		},
		{
			name: "monitor role collides with a service grant",
			res: types.Resources{
				DatabaseCluster: []types.DatabaseClusterSpec{{Name: "pg", Host: "edge"}},
				Service:         []types.ServiceSpec{svc("pg", "otelmon")},
			},
			wantErr: true,
		},
		{
			name:    "the global/ prefix is stripped before the name is derived",
			res:     types.Resources{Service: []types.ServiceSpec{svc("api-eu", "global/ddns"), svc("api", "eu-ddns")}},
			wantErr: true,
		},
		{
			// The longest name that survives Postgres intact: the derived role is exactly
			// pgMaxIdentifierLen bytes, so nothing is truncated.
			name: "a role name of exactly the identifier limit passes",
			res:  types.Resources{Service: []types.ServiceSpec{svc(strings.Repeat("a", 20), strings.Repeat("b", 18))}},
		},
		{
			// One byte over: CREATE ROLE would mint the 63-byte truncation while the
			// connection URL carries the full name, so the service could never log in.
			name:    "a role name one byte over the identifier limit is rejected",
			res:     types.Resources{Service: []types.ServiceSpec{svc(strings.Repeat("a", 20), strings.Repeat("b", 19))}},
			wantErr: true,
		},
		{
			// Two DISTINCT derived names that share a 63-byte prefix: the uniqueness map
			// sees two different strings, but Postgres would truncate both to one role —
			// only the length rule catches this.
			name: "two names truncating to one role are rejected",
			res: types.Resources{Service: []types.ServiceSpec{
				svc(strings.Repeat("a", 20), strings.Repeat("b", 19)+"x"),
				svc(strings.Repeat("a", 20), strings.Repeat("b", 19)+"y"),
			}},
			wantErr: true,
		},
		{
			name:    "the monitor role is length-checked too",
			res:     types.Resources{DatabaseCluster: []types.DatabaseClusterSpec{{Name: strings.Repeat("c", 40), Host: "edge"}}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDBRoleNames(tc.res, "prd", "use1")
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), naming.Resource("prd", "use1", "dbrole", ""))
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestDBRoleNameLimitMatchesPostgres pins the boundary the length rule enforces to the
// real identifier the program hands Postgres: the accepted maximum is exactly
// pgMaxIdentifierLen bytes, so no accepted role name can be truncated server-side.
func TestDBRoleNameLimitMatchesPostgres(t *testing.T) {
	ok := dbRoleName("prd", "use1", strings.Repeat("a", 20), strings.Repeat("b", 18))
	require.Len(t, ok, pgMaxIdentifierLen)
	require.NoError(t, checkDBRoleNames(types.Resources{Service: []types.ServiceSpec{
		{Name: strings.Repeat("a", 20), Grants: []types.GrantSpec{{Resource: "database/" + strings.Repeat("b", 18), Permission: "rw"}}},
	}}, "prd", "use1"))
}

// TestBackupCredentialTriggerIsNotSecret: the R2 credential write script carries both
// AWS_* keys — Triggers must carry only its safeTrigger hash.
func TestBackupCredentialTriggerIsNotSecret(t *testing.T) {
	mocks := newCommandMocks()
	res, computeOut := obsResources()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		access := pulumi.ToSecret(pulumi.String("AKIA-x")).(pulumi.StringOutput)
		secret := pulumi.ToSecret(pulumi.String("s3cr3t")).(pulumi.StringOutput)
		return provisionDatabaseBackups(ctx, res, computeOut, nil, "bucket", "https://r2.example.com", true, access, secret, "priv", "prd", "use1")
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	cred := mocks.captured[naming.Resource("prd", "use1", "dbbackup", "edge-01")+"-credential"]
	require.NotEmpty(t, cred.create, "the R2 credential write must be registered")
	require.Len(t, cred.triggers, 1)
	assert.False(t, cred.triggersSecret, "a secret must never enter Triggers")
	assert.Equal(t, sha256Hex(cred.create), cred.triggers[0],
		"Triggers must carry the safeTrigger hash of the credential script, not the script")
}
