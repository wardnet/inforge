package program

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/postgres"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/service"
	"github.com/wardnet/inforge/internal/types"
)

// ADR-0042: a command whose Delete script destroys host state must never carry
// Triggers — a Triggers diff is a REPLACE, and DeleteBeforeReplace then runs the
// delete RECORDED IN STATE by the previous deploy (which a fix in the current
// release cannot have refreshed yet: the v6.1.0 outage). Idempotent script
// changes re-run in place via the create/update diff instead.

// The per-service DB-role mint drops a live credential on delete; it must be
// trigger-free so a mint-SQL change is an in-place re-mint, never a replace.
func TestServiceRoleMintCarriesNoTriggers(t *testing.T) {
	mocks := newCommandMocks()
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
		_, err = p.ProvisionRole(ctx, "ghost-role", "rw")
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	mint, ok := mocks.captured["ghost-role-mint"]
	require.True(t, ok, "the role mint must be registered")
	assert.Empty(t, mint.triggers, "the role mint must carry no Triggers (ADR-0042)")
	assert.Contains(t, mint.deleteScript, "DROP ROLE",
		"the mint keeps its drop as the Delete input — it runs only on true removal")
}

// The -secrets command's old delete removed descriptor.yaml + secrets.age, and it
// ran on every Triggers-driven replace — leaving every service one restart from
// death whenever a deploy aborted mid-replace. The command must be delete-free;
// true-removal cleanup lives in serviceDeprovisionScript.
func TestSecretsCommandCarriesNoDelete(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		host := types.ComputeOutputs{PublicIP: pulumi.String("1.2.3.4").ToStringOutput()}
		gate, err := cloudInitGate(ctx, map[string]pulumi.Resource{}, "bridge-01", host, "priv", "prd", "use1")
		if err != nil {
			return err
		}
		svc := types.ServiceSpec{Name: "ghost", Container: "ghost", Host: "bridge-01", Type: "raw", User: "ghost"}
		env := map[string]pulumi.StringOutput{"TOKEN": pulumi.String("v").ToStringOutput()}
		return deliverServiceSecrets(ctx, svc, host, serviceMaterial{Env: env}, "deploy", "priv", "prd", "us-east-1", "use1", "example.com", "bridge-01", 0, gate, nil)
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	got, ok := mocks.captured[naming.Resource("prd", "use1", "svc", "ghost")+"-secrets"]
	require.True(t, ok, "the secrets command must be registered")
	assert.Empty(t, got.deleteScript, "the -secrets command must carry no Delete (ADR-0042)")
	require.Len(t, got.triggers, 2,
		"Triggers stays the SOLE change detector (descriptor + plaintext hash) — the write script embeds a fresh age ciphertext every run, so create/update diffs are ignored")
}

// The unit's delete is the single owner of on-host service teardown: unit,
// descriptor AND secrets.age. If the agent inputs were missing here, removing a
// service from the manifest would leak them — or worse, a future refactor could
// reintroduce a second deleter that runs on replace.
func TestServiceDeprovisionRemovesAgentInputs(t *testing.T) {
	svc := types.ServiceSpec{Name: "ghost"}
	got := serviceDeprovisionScript(svc)
	for _, want := range []string{
		service.UnitPath("ghost"),
		service.DescriptorPath("ghost"),
		service.SecretsPath("ghost"),
	} {
		assert.Contains(t, got, want, "deprovision must remove %s", want)
	}
}

// A provision change (agent version bump, unit edit) is an in-place UPDATE
// (ADR-0042) — so the script itself must move the running service onto the new
// binary/unit. try-restart restarts only an active unit: a not-yet-released
// service stays down, and the first CREATE is a no-op.
func TestServiceProvisionScriptTryRestartsAfterEnable(t *testing.T) {
	svc := types.ServiceSpec{Name: "ghost", Container: "ghost", Type: "raw", User: "ghost"}
	got := serviceProvisionScript(svc, "9.9.9")
	enable := strings.Index(got, "systemctl enable --now")
	restart := strings.Index(got, "systemctl try-restart")
	require.Greater(t, enable, -1, "provision must enable the unit")
	require.Greater(t, restart, -1, "provision must try-restart so an update moves the running service onto the new binary/unit")
	assert.Greater(t, restart, enable, "try-restart must come after enable --now")
}
