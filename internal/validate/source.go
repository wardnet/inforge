package validate

import (
	"github.com/wardnet/inforge/internal/yamldoc"
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

// ParseSource parses a secrets source DSL value into its structured form for
// VALIDATION. It only checks grammar; whether a ref resolves to a real
// resource/output is checked separately during cross-resource validation, which is
// this package's real job and needs the whole resource graph — not something a
// resolver can know.
//
// The grammar itself lives in internal/yamldoc, with the reader: it is ONE DSL,
// shared by every file, and the deploy program dispatches on the same schemes to
// build a service's values (program/secretresolve.go). This function does not
// re-implement it — it names the same schemes and reuses the same parsers, so the
// thing validate accepts and the thing deploy resolves can never drift apart.
func ParseSource(s string) (Source, error) {
	if ref, ok, err := yamldoc.ParseRef(s); ok {
		if err != nil {
			return Source{}, err
		}
		return Source{
			Kind:      SourceRef,
			RefType:   ref.Type,
			RefName:   ref.Name,
			RefOutput: ref.Output,
		}, nil
	}
	if key, ok, err := yamldoc.ParseKey(yamldoc.VaultScheme, s); ok {
		if err != nil {
			return Source{}, err
		}
		return Source{Kind: SourceVault, VaultKey: key}, nil
	}
	if name, ok, err := yamldoc.ParseKey(yamldoc.EnvScheme, s); ok {
		if err != nil {
			return Source{}, err
		}
		return Source{Kind: SourceEnv, EnvName: name}, nil
	}
	// Any other string is a literal value.
	return Source{Kind: SourceLiteral, LiteralValue: s}, nil
}
