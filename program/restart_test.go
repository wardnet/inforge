package program

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/types"
)

// The production failure this file exists to prevent:
//
// serviceProvisionScript pins the inforge-agent version, so EVERY inforge release
// changes it → the provision resource's Triggers change → it is REPLACED, and
// DeleteBeforeReplace runs `disable --now` + `rm -f <unit>` FIRST. The delivery
// command (descriptor/secrets + reloadOrRestartScript) had no ordering dependency on
// it, so `systemctl restart` could land inside that window, fail with "Unit not
// found", get swallowed by `2>/dev/null || true` — and the service stayed STOPPED
// while the deploy reported success. wardnet's tenants went down exactly this way on
// the 5.5.1 → 5.5.2 deploy: unit enabled, inactive, descriptor freshly written, no
// error anywhere.
//
// Three invariants close it, one test each.

// 1. Ordering: delivery (which restarts) must never run before the unit exists.
func TestDeliveryDependsOnUnitProvision(t *testing.T) {
	for _, tc := range []struct {
		name     string
		material map[string]serviceMaterial
	}{
		{"secret-bearing service", map[string]serviceMaterial{
			"ghost": {Env: map[string]pulumi.StringOutput{"TOKEN": pulumi.String("t").ToStringOutput()}},
		}},
		{"secret-less service", map[string]serviceMaterial{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mocks := newCommandMocks()
			err := pulumi.RunErr(func(ctx *pulumi.Context) error {
				res := types.Resources{
					Compute: []types.ComputeSpec{
						{Name: "bridge", InstanceCount: 1, DeployUser: &types.DeployUserSpec{Name: "deploy"}},
					},
					Service: []types.ServiceSpec{
						{Name: "ghost", Container: "ghost", Host: "bridge-01", Type: "raw", User: "ghost"},
					},
				}
				computeOut := map[string]types.ComputeOutputs{
					"bridge-01": {PublicIP: pulumi.String("1.2.3.4").ToStringOutput()},
				}
				return provisionServices(ctx, res, computeOut, tc.material, map[string]pulumi.Resource{},
					"priv", "prd", "us-east-1", "use1", "example.com", "1.2.3", nil)
			}, pulumi.WithMocks("project", "stack", mocks))
			require.NoError(t, err)

			svc := naming.Resource("prd", "use1", "svc", "ghost")
			assert.True(t, mocks.dependsOn(svc+"-secrets", svc+"-provision"),
				"the delivery command restarts the unit, so it MUST wait for the unit to exist — "+
					"otherwise it races the `rm -f <unit>` of a provision replace and the service stays down")
		})
	}
}

// 2. A failed restart must fail the deploy. Swallowing it is what turned a dead
// service into a green apply.
func TestReloadOrRestartScriptIsNotBestEffort(t *testing.T) {
	got := reloadOrRestartScript(types.ServiceSpec{Name: "tenants"})
	assert.Contains(t, got, "systemctl restart")
	assert.NotContains(t, got, "|| true", "a swallowed restart failure hides a stopped service behind a successful deploy")
	assert.NotContains(t, got, "2>/dev/null", "the restart's stderr is the only evidence of why it failed")

	// A service declaring reload: still reloads rather than restarting (no downtime).
	got = reloadOrRestartScript(types.ServiceSpec{Name: "tunneller", Reload: "kill -HUP $MAINPID"})
	assert.Contains(t, got, "systemctl reload")
	assert.NotContains(t, got, "|| true")
}

// 2b. `systemctl reload` on an INACTIVE unit is a hard error ("Unit is not active,
// cannot reload") — ConditionPathExists only saves a start/restart, never a reload.
// A reload: service that has never had a release (unit enabled, condition unmet,
// inactive) therefore failed the whole deploy at its delivery command. Guard the
// reload on is-active, and start the unit when it is not running — never skip it,
// or a service stopped mid-deploy would be left stopped behind a green apply.
func TestReloadIsGuardedOnActiveUnit(t *testing.T) {
	for _, svc := range []types.ServiceSpec{
		{Name: "tunneller", Reload: "kill -HUP $MAINPID"},
		{Name: "tenants"},
	} {
		got := reloadOrRestartScript(svc)
		assert.True(t, strings.HasPrefix(got, "if systemctl is-active --quiet "),
			"the signal must branch on whether the unit is running, got: %s", got)
		assert.Contains(t, got, "systemctl start",
			"a not-running unit must be STARTED (ConditionPathExists makes that a no-op "+
				"before the first release), never skipped — skipping leaves it stopped: %s", got)
		assert.NotContains(t, got, "|| true")
	}
}

// 3. The provision script is also the CREATE half of a replace whose DELETE half ran
// `disable --now`. Enabling without starting leaves a previously-running service
// stopped.
func TestServiceProvisionScriptStartsTheUnit(t *testing.T) {
	got := serviceProvisionScript(types.ServiceSpec{Name: "tenants", User: "tenants"}, "5.5.2")
	assert.Contains(t, got, "systemctl enable --now",
		"enable alone leaves a replaced (disable --now'd) service stopped; --now restores it")

	// ConditionPathExists in the unit makes `enable --now` a no-op — exit 0, not a
	// failure — on a first deploy before any release has landed the binary, which is
	// what the old `enable` + best-effort restart was working around.
	assert.True(t, strings.Contains(got, "systemctl daemon-reload"), "the unit must be reloaded before it is enabled")
}
