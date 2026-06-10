package validate

import (
	"fmt"
	"regexp"
	"strings"
)

// SourceKind distinguishes the forms of a secrets source.
type SourceKind int

const (
	// SourceRef is a reference to another resource's output:
	// ref:<type>/<name>.<output>.
	SourceRef SourceKind = iota
	// SourceEnv is an environment-variable reference: ${NAME}. The value is read
	// from the deploy process environment (the same ${ENV_VAR} convention used in
	// variables.yaml/regions.yaml), so a consumer injects it however they like —
	// e.g. a GitHub Actions secret mapped to an env var in their workflow.
	SourceEnv
	// SourceStatic is a literal value authored inline: static:<value> (or the
	// value:<value> alias). The value is stored in plaintext in the resource file,
	// so it is for non-secret configuration, not real secrets.
	SourceStatic
)

// Source is a parsed secrets source DSL value.
type Source struct {
	Kind SourceKind
	// Ref fields (Kind == SourceRef).
	RefType   string // "database" | "compute"
	RefName   string // resource name (compute uses an expanded specKey)
	RefOutput string // output token, e.g. "connectionUrl" | "publicIp"
	// EnvName field (Kind == SourceEnv): the environment variable name.
	EnvName string
	// StaticValue field (Kind == SourceStatic): the literal value, verbatim.
	StaticValue string
}

var (
	refPattern = regexp.MustCompile(`^ref:(database|compute)/(.+)\.([^.]+)$`)
	envPattern = regexp.MustCompile(`^\$\{([A-Z_][A-Z0-9_]*)\}$`)
)

// ParseSource parses a secrets source value into its structured form. It only
// checks grammar; whether a ref resolves to a real resource/output is checked
// separately during cross-resource validation.
func ParseSource(s string) (Source, error) {
	if m := refPattern.FindStringSubmatch(s); m != nil {
		return Source{
			Kind:      SourceRef,
			RefType:   m[1],
			RefName:   m[2],
			RefOutput: m[3],
		}, nil
	}
	if m := envPattern.FindStringSubmatch(s); m != nil {
		return Source{Kind: SourceEnv, EnvName: m[1]}, nil
	}
	// static:<value> / value:<value> — a literal. The value is taken verbatim (any
	// characters), so prefix-strip rather than pattern-match; reject an empty value,
	// which is always a mistake and would fail the service start later anyway.
	for _, prefix := range []string{"static:", "value:"} {
		if v, ok := strings.CutPrefix(s, prefix); ok {
			if v == "" {
				return Source{}, fmt.Errorf("invalid source %q: a %s value must not be empty", s, prefix)
			}
			return Source{Kind: SourceStatic, StaticValue: v}, nil
		}
	}
	return Source{}, fmt.Errorf("invalid source %q: want ref:<database|compute>/<name>.<output>, ${ENV_VAR}, or static:<value>", s)
}
