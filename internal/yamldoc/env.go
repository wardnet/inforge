package yamldoc

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// EnvResolver resolves `env:NAME` against the environment.
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

// Matches claims any leaf written as env:NAME. A leaf without the scheme is not
// this resolver's business — it is a static value and comes back as written.
func (EnvResolver) Matches(raw string) bool { return strings.HasPrefix(raw, EnvScheme) }

// Resolve reads the named variable. An unset variable is an error, and so is one
// set to the empty string: a blank credential is never a legitimate value, and
// passing "" on to a provider fails far from the cause.
func (e EnvResolver) Resolve(_ context.Context, raw string) (string, error) {
	key, _, err := ParseKey(EnvScheme, raw)
	if err != nil {
		return "", err
	}
	v, ok := e.lookup(key)
	if !ok || v == "" {
		return "", fmt.Errorf("missing required env var: %s", key)
	}
	return v, nil
}
