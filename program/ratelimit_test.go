package program

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wardnet/inforge/internal/types"
)

// stampFixture builds a minimal edge: one service+app behind ingress "main", plus a
// gateway "gw", each surfaced as one derived server on host "h".
func stampFixture() (types.Resources, map[string][]types.IngressRoute, map[string][]types.IngressApp, map[string][]types.IngressGateway) {
	res := types.Resources{
		Service: []types.ServiceSpec{{Name: "api", Ingress: "main"}},
		App:     []types.AppSpec{{Name: "web", Ingress: "main"}},
		Ingress: []types.IngressSpec{{Name: "main"}},
		Gateway: []types.GatewaySpec{{Name: "gw"}},
	}
	routes := map[string][]types.IngressRoute{"h": {{Service: "api"}}}
	apps := map[string][]types.IngressApp{"h": {{Name: "web"}}}
	gws := map[string][]types.IngressGateway{"h": {{Name: "gw"}}}
	return res, routes, apps, gws
}

func enabledSec() types.SecurityConfig {
	return types.SecurityConfig{RateLimit: types.RateLimitConfig{
		Enabled: true, RequestsPerSecond: 20, Burst: 40, MaxConnections: 40,
	}}
}

func TestStampRateLimitAppliesUniformly(t *testing.T) {
	res, routes, apps, gws := stampFixture()
	stampRateLimit(enabledSec(), res, routes, apps, gws)
	assert.NotNil(t, routes["h"][0].RateLimit)
	assert.NotNil(t, apps["h"][0].RateLimit)
	assert.NotNil(t, gws["h"][0].RateLimit)
	// Same fixed-stem profile everywhere.
	assert.Equal(t, types.RateLimitZoneStem, routes["h"][0].RateLimit.Name)
	assert.Equal(t, 20, routes["h"][0].RateLimit.RPS)
	assert.Equal(t, 40, gws["h"][0].RateLimit.MaxConn)
}

func TestStampRateLimitDisabledStampsNothing(t *testing.T) {
	res, routes, apps, gws := stampFixture()
	stampRateLimit(types.SecurityConfig{}, res, routes, apps, gws)
	assert.Nil(t, routes["h"][0].RateLimit)
	assert.Nil(t, apps["h"][0].RateLimit)
	assert.Nil(t, gws["h"][0].RateLimit)
}

func TestStampRateLimitIngressOptOut(t *testing.T) {
	res, routes, apps, gws := stampFixture()
	res.Ingress[0].Security = ptrBool(false) // this ingress opts out
	stampRateLimit(enabledSec(), res, routes, apps, gws)
	assert.Nil(t, routes["h"][0].RateLimit, "route on an opted-out ingress is not limited")
	assert.Nil(t, apps["h"][0].RateLimit, "app on an opted-out ingress is not limited")
	assert.NotNil(t, gws["h"][0].RateLimit, "the gateway's own edge (its ingress) did not opt out, so its termination stays limited")
}

func TestStampRateLimitGatewayOptOut(t *testing.T) {
	res, routes, apps, gws := stampFixture()
	res.Gateway[0].Security = ptrBool(false)
	stampRateLimit(enabledSec(), res, routes, apps, gws)
	assert.NotNil(t, routes["h"][0].RateLimit)
	assert.Nil(t, gws["h"][0].RateLimit, "an opted-out gateway is not limited")
}

// security: true is an explicit opt-IN (identical to absent), never an opt-out.
func TestStampRateLimitExplicitTrueStillApplies(t *testing.T) {
	res, routes, apps, gws := stampFixture()
	res.Ingress[0].Security = ptrBool(true)
	stampRateLimit(enabledSec(), res, routes, apps, gws)
	assert.NotNil(t, routes["h"][0].RateLimit)
}
