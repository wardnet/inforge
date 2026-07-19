package validate

import (
	"fmt"
	"path/filepath"

	"github.com/wardnet/inforge/internal/types"
)

// checkSecurity validates the env-level edge security tier authoring (ADR-0043): the
// blanket rate-limit bounds. It reads only the literal variables.yaml structure — no
// secrets, no environment. A disabled or absent block is a no-op. The per-edge
// `security:` opt-out is a plain boolean enforced by the ingress/gateway JSON schema, so
// it needs nothing here.
func checkSecurity(r *reporter, dir, env string, sec types.SecurityConfig) {
	rl := sec.RateLimit
	if !rl.Enabled {
		return
	}
	var errs []string
	if rl.RequestsPerSecond < 0 {
		errs = append(errs, fmt.Sprintf("security.rate_limit.requests_per_second must be >= 0, got %d", rl.RequestsPerSecond))
	}
	if rl.Burst < 0 {
		errs = append(errs, fmt.Sprintf("security.rate_limit.burst must be >= 0, got %d", rl.Burst))
	}
	if rl.MaxConnections < 0 {
		errs = append(errs, fmt.Sprintf("security.rate_limit.max_connections must be >= 0, got %d", rl.MaxConnections))
	}
	if rl.RequestsPerSecond == 0 && rl.MaxConnections == 0 {
		errs = append(errs, "security.rate_limit.enabled is true but both requests_per_second and max_connections are 0 — nothing to limit (set one, or enabled: false)")
	}
	if len(errs) > 0 {
		r.fail(filepath.Join(dir, env, "variables.yaml"), errs...)
	}
}
