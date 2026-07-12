package yamldoc

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// envPattern matches the ${NAME} references an operator writes in place of a
// value that must come from the environment.
var envPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// EnvResolver resolves ${NAME} against the environment. It is the first
// implementation of Resolver and, for now, the only one — environment.yaml's
// env:/vault:/ref: sources are the same shape (match a pattern, fetch a value)
// and join this chain next.
type EnvResolver struct {
	// lookup is the environment. Injectable so a caller — or a test — can resolve
	// against something other than the ambient process environment.
	lookup func(string) (string, bool)
}

// Env resolves against the process environment.
func Env() EnvResolver { return EnvResolver{lookup: os.LookupEnv} }

// EnvFrom resolves against an explicit lookup.
func EnvFrom(lookup func(string) (string, bool)) EnvResolver {
	return EnvResolver{lookup: lookup}
}

func (EnvResolver) Name() string { return "env" }

// Matches claims any leaf carrying a ${...} reference. A leaf without one is not
// this resolver's business: it is a static value and comes back as written.
func (EnvResolver) Matches(raw string) bool { return envPattern.MatchString(raw) }

// Resolve expands every ${NAME} in raw. An unset variable is an error, and so is
// one set to the empty string: a blank credential is never a legitimate value, and
// passing "" on to a provider fails far from the cause.
func (e EnvResolver) Resolve(_ context.Context, raw string) (string, error) {
	var missing []string
	out := envPattern.ReplaceAllStringFunc(raw, func(m string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(m, "${"), "}")
		v, ok := e.lookup(key)
		if !ok || v == "" {
			missing = append(missing, key)
			return ""
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing required env var: %s", strings.Join(missing, ", "))
	}
	return out, nil
}
