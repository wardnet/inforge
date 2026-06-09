package bootstrapper

import (
	"fmt"
	"sort"
	"strings"
)

// minimalPATH is the explicit PATH given to the service. The bootstrapper does
// not inherit its own environment — the child gets only this base plus the
// service's secrets, so nothing from the root context leaks in.
const minimalPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// ReservedEnvPrefix is the environment-variable namespace inforge owns and
// injects itself (the INFORGE_DEPLOYMENT_* deployment context). A service must not
// map a secret to a name under this prefix — validation rejects it up front, and
// buildEnv rejects it as a backstop — so an injected value can never silently
// shadow (or be shadowed by) a service secret.
const ReservedEnvPrefix = "INFORGE_"

// buildEnv builds the environment slice for the service process: a minimal,
// explicit base (PATH, HOME, USER, LOGNAME) followed by each descriptor env var
// resolved against the fetched secrets. Every mapped var must resolve to a
// non-empty value or the start fails — a 200 with a missing/empty key must never
// exec the service with a blank secret. Secret values appear only in this slice
// (passed to syscall.Exec as envv), never in argv, on disk, or in the journal.
func buildEnv(d Descriptor, secrets map[string]string, home string) ([]string, error) {
	env := []string{
		"PATH=" + minimalPATH,
		"HOME=" + home,
		"USER=" + d.User,
		"LOGNAME=" + d.User,
	}

	// Deployment context (region/env/domain/namespace/fqdn) injected as
	// INFORGE_DEPLOYMENT_* — non-secret, present for secret-less services too. The
	// names are reserved: a service must not map a secret to one of them.
	if dpl := d.Deployment; dpl != (Deployment{}) {
		env = append(env,
			"INFORGE_DEPLOYMENT_REGION="+dpl.Region,
			"INFORGE_DEPLOYMENT_REGION_SLUG="+dpl.RegionSlug,
			"INFORGE_DEPLOYMENT_ENVIRONMENT="+dpl.Environment,
			"INFORGE_DEPLOYMENT_BASE_DOMAIN="+dpl.BaseDomain,
			"INFORGE_DEPLOYMENT_NAMESPACE="+dpl.Namespace,
			"INFORGE_DEPLOYMENT_FQDN="+dpl.FQDN,
		)
	}

	names := make([]string, 0, len(d.Env))
	for name := range d.Env {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		// Backstop the validate-time rule: a secret must never claim a reserved
		// INFORGE_* name, or it would collide with the injected deployment context
		// (duplicate env entries with unspecified precedence). Fail the start loudly.
		if strings.HasPrefix(name, ReservedEnvPrefix) {
			return nil, fmt.Errorf("env var %s uses the reserved %s* namespace owned by inforge", name, ReservedEnvPrefix)
		}
		ref := d.Env[name]
		val, ok := secrets[ref]
		if !ok || val == "" {
			return nil, fmt.Errorf("secret %q for env var %s not found or empty", ref, name)
		}
		env = append(env, name+"="+val)
	}
	return env, nil
}
