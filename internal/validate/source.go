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
	// SourceEnv is an environment-variable reference: env:NAME. The value is read
	// from the deploy process environment, so a consumer injects it however they
	// like — e.g. a GitHub Actions secret mapped to an env var in their workflow.
	SourceEnv
	// SourceVault is a value stored age-encrypted in the environment's committed
	// secret store (resources/<env>/secrets.enc.yaml), keyed by the spec's
	// container and the vault key name carried in the source string:
	// vault:<KEY>. The vault key is decoupled from the env var name, so
	// DATABASE_URL: "vault:PROD_DB_URL" is valid.
	SourceVault
	// SourceLiteral is a verbatim inline value: any string that does not match
	// a recognised prefix. Use for non-secret configuration; never for real
	// secrets (the value is committed in plaintext).
	SourceLiteral
)

// Source is a parsed secrets source DSL value.
type Source struct {
	Kind SourceKind
	// Ref fields (Kind == SourceRef).
	RefType   string // "database" | "compute" (database refs are rejected — credentials flow via grants)
	RefName   string // resource name (compute uses an expanded specKey)
	RefOutput string // output token, e.g. "publicIp" (compute); a database exposes none
	// EnvName field (Kind == SourceEnv): the environment variable name.
	EnvName string
	// VaultKey field (Kind == SourceVault): key name in the encrypted store.
	VaultKey string
	// LiteralValue field (Kind == SourceLiteral): the verbatim string value.
	LiteralValue string
}

var refPattern = regexp.MustCompile(`^ref:(database|compute)/(.+)\.([^.]+)$`)

// ParseSource parses a secrets source DSL value into its structured form. It
// only checks grammar; whether a ref resolves to a real resource/output is
// checked separately during cross-resource validation.
//
// Source DSL:
//
//	ref:<database|compute>/<name>.<output>   — resource FK reference
//	vault:<KEY>                              — encrypted store key
//	env:<VAR>                                — deploy-time environment variable
//	<anything else>                          — literal value (plaintext in git)
func ParseSource(s string) (Source, error) {
	if m := refPattern.FindStringSubmatch(s); m != nil {
		return Source{
			Kind:      SourceRef,
			RefType:   m[1],
			RefName:   m[2],
			RefOutput: m[3],
		}, nil
	}
	if v, ok := strings.CutPrefix(s, "vault:"); ok {
		if v == "" {
			return Source{}, fmt.Errorf("invalid source %q: vault: requires a key name, e.g. vault:MY_KEY", s)
		}
		return Source{Kind: SourceVault, VaultKey: v}, nil
	}
	if v, ok := strings.CutPrefix(s, "env:"); ok {
		if v == "" {
			return Source{}, fmt.Errorf("invalid source %q: env: requires a variable name, e.g. env:MY_VAR", s)
		}
		return Source{Kind: SourceEnv, EnvName: v}, nil
	}
	// Any other string is a literal value.
	return Source{Kind: SourceLiteral, LiteralValue: s}, nil
}
