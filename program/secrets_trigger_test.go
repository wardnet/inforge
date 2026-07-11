package program

import (
	"testing"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/naming"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/types"
)

// TestServiceSecretsTriggerIsNotSecret guards the ADR-0035 secrets-write command
// against a regression that broke `inforge preview` on v5.0.0: a service whose
// resolved env carries a secret value (e.g. a grant's random DB password) built a
// secret-tainted Triggers hash. A secret + an unknown value (the password is
// unknown at preview) marshals to the Pulumi engine error
// `malformed RPC secret: missing value for "triggers"`, aborting the whole preview.
// Triggers must therefore never be secret.
func TestServiceSecretsTriggerIsNotSecret(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		host := types.ComputeOutputs{PublicIP: pulumi.String("1.2.3.4").ToStringOutput()}
		gate, err := cloudInitGate(ctx, map[string]pulumi.Resource{}, "bridge-01", host, "priv", "prd", "use1")
		if err != nil {
			return err
		}
		svc := types.ServiceSpec{Name: "ghost", Container: "ghost", Host: "bridge-01", Type: "raw", User: "ghost"}
		// A single secret env value is enough to taint the Triggers hash.
		secretEnv := map[string]pulumi.StringOutput{
			"TOKEN": pulumi.ToSecret(pulumi.String("s3cr3t")).(pulumi.StringOutput),
		}
		return deliverServiceSecrets(ctx, svc, host, serviceMaterial{Env: secretEnv}, "deploy", "priv", "prd", "us-east-1", "use1", "example.com", "bridge-01", 0, gate)
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	name := naming.Resource("prd", "use1", "svc", "ghost") + "-secrets"
	got, ok := mocks.captured[name]
	require.True(t, ok, "the secrets-write command must be registered")
	assert.False(t, got.triggersSecret,
		"a secret must never enter Triggers — a secret+unknown trigger breaks preview with 'malformed RPC secret'")
}

// TestSafeTriggerStripsSecret is the unit guard for the shared helper both the
// service-secrets write and the DB role mint use to keep secrets out of Triggers.
// The raw-secret probe proves the mock detects a secret trigger (so the safe probe
// failing to strip would be caught); the safe probe proves safeTrigger removes the
// marker. Together they lock the invariant "no secret in Triggers".
func TestSafeTriggerStripsSecret(t *testing.T) {
	mocks := newCommandMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		secret := pulumi.ToSecret(pulumi.String("s3cr3t")).(pulumi.StringOutput)
		conn := iremote.Connection(pulumi.String("1.2.3.4"), "deploy", "priv")
		if _, err := remote.NewCommand(ctx, "raw-probe", &remote.CommandArgs{
			Connection: conn,
			Create:     pulumi.String("true"),
			Triggers:   pulumi.Array{secret},
		}); err != nil {
			return err
		}
		_, err := remote.NewCommand(ctx, "safe-probe", &remote.CommandArgs{
			Connection: conn,
			Create:     pulumi.String("true"),
			Triggers:   pulumi.Array{safeTrigger(secret)},
		})
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	assert.True(t, mocks.captured["raw-probe"].triggersSecret,
		"sanity: a raw secret in Triggers is detected as secret (the bug shape)")
	assert.False(t, mocks.captured["safe-probe"].triggersSecret,
		"safeTrigger must strip the secret marker so Triggers is safe to marshal")
}
