package program

import (
	"context"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/internal/yamldoc"
)

// await drains a StringOutput to its value. resolveRef builds its outputs from
// plain values (or from a compute output that is already resolved in these tests),
// so no provider run is needed.
func await(t *testing.T, out pulumi.StringOutput) string {
	t.Helper()
	done := make(chan string, 1)
	out.ApplyT(func(v string) string { done <- v; return v })
	select {
	case v := <-done:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("output never resolved")
		return ""
	}
}

func outputs() types.AllOutputs {
	return types.AllOutputs{
		Encrypted: map[string]map[string]string{
			"tenants": {"STRIPE_SECRET_KEY": "sk_live_abc"},
		},
		Compute: map[string]map[string]types.ComputeOutputs{
			"eu-central": {"edge": {PublicIP: pulumi.String("1.2.3.4").ToStringOutput()}},
		},
	}
}

func TestResolveRefEnv(t *testing.T) {
	t.Setenv("INFORGE_TEST_SOURCE", "from-the-environment")
	got, err := resolveRef("env:INFORGE_TEST_SOURCE", "tenants", "eu-central", outputs())
	require.NoError(t, err)
	assert.Equal(t, "from-the-environment", await(t, got))
}

func TestResolveRefEnvUnsetFailsLoudly(t *testing.T) {
	t.Setenv("INFORGE_TEST_SOURCE", "")
	_, err := resolveRef("env:INFORGE_TEST_SOURCE", "tenants", "eu-central", outputs())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty or unset",
		"an unset variable must fail, never materialise an empty secret")
}

func TestResolveRefVaultServesTheServicesOwnContainer(t *testing.T) {
	got, err := resolveRef("vault:STRIPE_SECRET_KEY", "tenants", "eu-central", outputs())
	require.NoError(t, err)
	assert.Equal(t, "sk_live_abc", await(t, got))
}

// A service reaches its OWN secrets and no other's. The chain is built from the
// DECLARING service (ADR-0040), so a co-located sibling — one that used to share a
// container namespace, and with it the blast radius — cannot read this key at all.
func TestResolveRefVaultCannotReachAnotherServicesSecret(t *testing.T) {
	_, err := resolveRef("vault:STRIPE_SECRET_KEY", "ddns", "eu-central", outputs())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `service "ddns"`)
}

func TestResolveRefCompute(t *testing.T) {
	got, err := resolveRef("ref:compute/edge.publicIp", "tenants", "eu-central", outputs())
	require.NoError(t, err)
	assert.Equal(t, "1.2.3.4", await(t, got))
}

// ADR-0025: a database exposes no referenceable outputs — DB credentials flow only
// through a grant. validate rejects this earlier; the deploy path guards it too.
func TestResolveRefDatabaseIsRejected(t *testing.T) {
	_, err := resolveRef("ref:database/tenants.url", "tenants", "eu-central", outputs())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "grants:")
}

func TestResolveRefUnknownComputeOutput(t *testing.T) {
	_, err := resolveRef("ref:compute/edge.privateIp", "tenants", "eu-central", outputs())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown compute output field")
}

// Anything claiming no scheme is a literal — non-secret config, committed verbatim.
func TestResolveRefLiteral(t *testing.T) {
	got, err := resolveRef("127.0.0.1:8080", "tenants", "eu-central", outputs())
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:8080", await(t, got))
}

// A MALFORMED reference under a recognised scheme must fail, not be mistaken for a
// literal — otherwise a typo'd `vault:` would be committed verbatim as the value,
// and the service would run with the string "vault:" as its secret.
func TestResolveRefMalformedReferenceFailsRatherThanBecomingALiteral(t *testing.T) {
	for _, src := range []string{"vault:", "env:", "ref:nonsense"} {
		_, err := resolveRef(src, "tenants", "eu-central", outputs())
		require.Error(t, err, "%q must not resolve to a literal", src)
	}
}

// THE CAPABILITY PROPERTY. What a reference can reach is decided by the CHAIN its
// caller passes, not by the file it appears in. A config file is decoded with an
// env-only chain, so a vault: reference there is claimed by nothing — it is an inert
// literal, not a secret leak. This is what lets one DSL be safe everywhere.
func TestAnEnvOnlyChainCannotReachTheVault(t *testing.T) {
	configChain := yamldoc.Chain[string]{yamldoc.Env()}

	_, claimed, err := configChain.Resolve(context.Background(), "vault:STRIPE_SECRET_KEY")
	require.NoError(t, err)
	assert.False(t, claimed, "no vault resolver in the chain ⇒ the reference is inert")

	// The very same text, resolved by a service's chain, IS a secret.
	got, err := resolveRef("vault:STRIPE_SECRET_KEY", "tenants", "eu-central", outputs())
	require.NoError(t, err)
	assert.Equal(t, "sk_live_abc", await(t, got))
}
