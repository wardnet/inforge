package yamldoc

import (
	"fmt"
	"regexp"
	"strings"
)

// The one authoring DSL, defined once. A reference is `<scheme>:<key>` — a WHOLE
// value, never an interpolation — and anything that claims no scheme is a static
// literal.
//
//	authorizedKeys:    env:SSH_AUTHORIZED_KEYS      # the deploy process environment
//	STRIPE_SECRET_KEY: vault:STRIPE_SECRET_KEY      # the env's committed secret store
//	NODE_IP:           ref:compute/edge.publicIp    # another resource's output
//	LOG_LEVEL:         info                         # a literal
//
// Which of these a document may actually reach is decided by the Chain its caller
// passes, not by the file it appears in.
const (
	EnvScheme   = "env:"
	VaultScheme = "vault:"
	RefScheme   = "ref:"
)

// refPattern matches ref:<type>/<name>.<output>. The name may itself contain a
// `global/` prefix (the one allowed cross-scope reference), hence the greedy middle.
var refPattern = regexp.MustCompile(`^ref:([a-z]+)/(.+)\.([^.]+)$`)

// Ref is a parsed ref: reference.
type Ref struct {
	Type   string // "compute" | "database"
	Name   string // resource name; may carry a "global/" prefix
	Output string // output token, e.g. "publicIp"
}

// ParseRef parses a ref: reference. ok is false when raw does not carry the ref:
// scheme at all; an error means it does, but is malformed — a typo'd reference must
// never be mistaken for a literal, or it would be committed verbatim as a value.
func ParseRef(raw string) (Ref, bool, error) {
	if !strings.HasPrefix(raw, RefScheme) {
		return Ref{}, false, nil
	}
	m := refPattern.FindStringSubmatch(raw)
	if m == nil {
		return Ref{}, true, fmt.Errorf("invalid source %q: want ref:<type>/<name>.<output>, e.g. ref:compute/edge.publicIp", raw)
	}
	return Ref{Type: m[1], Name: m[2], Output: m[3]}, true, nil
}

// ParseKey returns the key of a `<scheme>:<key>` reference. ok is false when raw
// does not carry the scheme; an error means it does, with nothing after the colon.
func ParseKey(scheme, raw string) (string, bool, error) {
	v, ok := strings.CutPrefix(raw, scheme)
	if !ok {
		return "", false, nil
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", true, fmt.Errorf("invalid source %q: %s requires a name, e.g. %sMY_KEY", raw, scheme, scheme)
	}
	return v, true, nil
}
