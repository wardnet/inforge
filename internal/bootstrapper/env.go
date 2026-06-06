package bootstrapper

import (
	"fmt"
	"sort"
)

// minimalPATH is the explicit PATH given to the service. The bootstrapper does
// not inherit its own environment — the child gets only this base plus the
// service's secrets, so nothing from the root context leaks in.
const minimalPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

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

	names := make([]string, 0, len(d.Env))
	for name := range d.Env {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		ref := d.Env[name]
		val, ok := secrets[ref]
		if !ok || val == "" {
			return nil, fmt.Errorf("secret %q for env var %s not found or empty", ref, name)
		}
		env = append(env, name+"="+val)
	}
	return env, nil
}
