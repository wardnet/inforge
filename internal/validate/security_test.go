package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wardnet/inforge/internal/types"
)

func secCfg(rl types.RateLimitConfig) types.SecurityConfig {
	return types.SecurityConfig{RateLimit: rl}
}

func TestCheckSecurityDisabledIsNoop(t *testing.T) {
	r := &reporter{}
	// Disabled, even with nonsense bounds, is not validated.
	checkSecurity(r, t.TempDir(), "prd", secCfg(types.RateLimitConfig{RequestsPerSecond: -5}))
	assert.False(t, r.failed)
}

func TestCheckSecurityValid(t *testing.T) {
	r := &reporter{}
	checkSecurity(r, t.TempDir(), "prd", secCfg(types.RateLimitConfig{
		Enabled: true, RequestsPerSecond: 20, Burst: 40, MaxConnections: 40,
	}))
	assert.False(t, r.failed)
}

func TestCheckSecurityRejectsNegativeBounds(t *testing.T) {
	for _, rl := range []types.RateLimitConfig{
		{Enabled: true, RequestsPerSecond: -1},
		{Enabled: true, RequestsPerSecond: 10, Burst: -1},
		{Enabled: true, MaxConnections: -1},
	} {
		r := &reporter{}
		checkSecurity(r, t.TempDir(), "prd", secCfg(rl))
		assert.True(t, r.failed, "expected failure for %+v", rl)
	}
}

func TestCheckSecurityRejectsEnabledButNothingToLimit(t *testing.T) {
	r := &reporter{}
	checkSecurity(r, t.TempDir(), "prd", secCfg(types.RateLimitConfig{Enabled: true}))
	assert.True(t, r.failed)
}

// A connection-only limit (rps 0, max_connections > 0) is valid — limit_conn alone.
func TestCheckSecurityConnectionOnlyIsValid(t *testing.T) {
	r := &reporter{}
	checkSecurity(r, t.TempDir(), "prd", secCfg(types.RateLimitConfig{Enabled: true, MaxConnections: 100}))
	assert.False(t, r.failed)
}
