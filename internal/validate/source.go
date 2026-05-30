package validate

import (
	"fmt"
	"regexp"
)

// SourceKind distinguishes the two forms of a secrets source.
type SourceKind int

const (
	// SourceRef is a reference to another resource's output:
	// ref:<type>/<name>.<output>.
	SourceRef SourceKind = iota
	// SourceGHA is a GitHub Actions secret reference: gha:<NAME>.
	SourceGHA
)

// Source is a parsed secrets source DSL value.
type Source struct {
	Kind SourceKind
	// Ref fields (Kind == SourceRef).
	RefType   string // "database" | "compute"
	RefName   string // resource name (compute uses an expanded specKey)
	RefOutput string // output token, e.g. "connectionUrl" | "publicIp"
	// GHA field (Kind == SourceGHA).
	GHAName string
}

var (
	refPattern = regexp.MustCompile(`^ref:(database|compute)/(.+)\.([^.]+)$`)
	ghaPattern = regexp.MustCompile(`^gha:([A-Z_][A-Z0-9_]*)$`)
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
	if m := ghaPattern.FindStringSubmatch(s); m != nil {
		return Source{Kind: SourceGHA, GHAName: m[1]}, nil
	}
	return Source{}, fmt.Errorf("invalid source %q: want ref:<database|compute>/<name>.<output> or gha:<NAME>", s)
}
