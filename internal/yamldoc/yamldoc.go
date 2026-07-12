// Package yamldoc is the single YAML reader in inforge. EVERY YAML file — the ones an
// operator authors and the ones inforge writes itself — is parsed here, into a document
// of nodes whose leaves are Scalars rather than strings. Nothing else in the codebase
// calls yaml.Unmarshal; TestYamldocIsTheOnlyReader enforces that, because a claim in a
// doc comment is not a guarantee.
//
// The reader NEVER resolves anything on its own.
//
// Resolution happens when a consumer asks for a real value, and only then. A
// Scalar is handed to a Chain of Resolvers; the first Resolver whose scheme claims
// the raw text resolves it, and a Scalar no Resolver claims is simply a static
// literal.
//
// One authoring DSL, every file: `<scheme>:<key>` — a whole-value reference, never
// an interpolation.
//
//	authorizedKeys: env:SSH_AUTHORIZED_KEYS
//	STRIPE_SECRET_KEY: vault:STRIPE_SECRET_KEY
//	DATABASE_URL: ref:database/tenants.url
//
// environment.yaml has spoken this DSL for years; variables.yaml and regions.yaml
// now speak it too. What a reference can reach is decided by the CHAIN the caller
// passes, not by which file it appears in — a vault: reference in a document decoded
// with an env-only chain resolves to nothing, because no resolver claims it. The
// caller owns the capability; the syntax merely names the mechanism.
//
// This is why the reader needs no exceptions, no lenient mode and no opt-out. A
// file whose leaves nobody resolves — an encrypted store, a Grafana dashboard, a
// descriptor inforge wrote itself — is read through the same reader and comes back
// verbatim, because nothing asked for a resolved value. Reading and resolving are
// separate acts.
//
// Four ways out of a Document, and picking one is where a caller says what a file's
// references may reach:
//
//	doc.Decode(&v)                       // leaves as written
//	doc.DecodeStrict(&v)                 // as written, and an unknown key is an error
//	doc.DecodeResolved(ctx, chain, &v)   // resolve every leaf it decodes — fail-fast, whole file
//	doc.At("base_domain")                // one leaf, resolved on demand
//
// Most files take Decode: a resource manifest, an encrypted store, a Grafana dashboard
// carrying its own ${DS_FOO} syntax, a descriptor inforge wrote — none of them resolve
// anything, and their leaves come back verbatim. Only variables.yaml and regions.yaml
// are decoded resolved (by the deploy program, which needs real credentials), and only
// a service's environment.yaml values go through a chain of their own.
//
// A command that touches real infrastructure decodes the whole file resolved, so
// a missing value is reported before anything is created. A command that reads
// one field resolves one field, and cannot be failed by a variable it never
// reads.
package yamldoc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Resolver turns the raw text of a leaf into its real value. Matches declares the
// scheme the Resolver claims — it is what makes a Chain work without the reader
// knowing anything about any particular syntax.
//
// It is generic in the value it produces, because different consumers of the SAME
// DSL need different values. A config file resolves to a string. The deploy program
// resolves a service's environment.yaml to a pulumi.StringOutput: a
// ref:compute/edge.publicIp is not known until the host exists, and a vault: value
// is marked secret so its plaintext is encrypted in Pulumi state. The grammar and
// the dispatch are shared; the value type is the consumer's.
type Resolver[T any] interface {
	// Name identifies the resolver in errors ("env", "vault", "ref").
	Name() string
	// Matches reports whether raw carries this resolver's scheme.
	Matches(raw string) bool
	// Resolve returns the real value. It is only called when Matches is true.
	Resolve(ctx context.Context, raw string) (T, error)
}

// Chain is an ordered set of Resolvers. The first one whose scheme claims a leaf
// resolves it; a leaf no Resolver claims is a static literal.
//
// The chain IS the capability: a vault: reference in a document decoded with an
// env-only chain resolves to nothing, because nothing claims it. What a file may
// reach is decided by its caller, not by which file it is.
type Chain[T any] []Resolver[T]

// Claim returns the first resolver whose scheme matches raw, if any. No match means
// the leaf is a static value, not a reference.
func (c Chain[T]) Claim(raw string) (Resolver[T], bool) {
	for _, r := range c {
		if r.Matches(raw) {
			return r, true
		}
	}
	return nil, false
}

// Resolve runs raw through the chain. claimed reports whether any resolver owned
// it; when false, v is the zero value and the caller decides what a static literal
// means for its value type (a string is itself; a pulumi value is pulumi.String).
func (c Chain[T]) Resolve(ctx context.Context, raw string) (v T, claimed bool, err error) {
	r, ok := c.Claim(raw)
	if !ok {
		return v, false, nil
	}
	v, err = r.Resolve(ctx, raw)
	if err != nil {
		return v, true, fmt.Errorf("%s: %w", r.Name(), err)
	}
	return v, true, nil
}

// Scalar is a leaf of a Document: the raw text as written, plus where it came
// from. It is deliberately NOT a string — a caller cannot use a leaf's value
// without deciding whether to resolve it, because the compiler will not let them.
type Scalar struct {
	raw  string
	path string // dotted path within the document, for errors
	line int
}

// Literal returns the leaf exactly as written, references unresolved. Use it when
// the text IS the value (an encrypted blob, a dashboard's own template syntax).
func (s Scalar) Literal() string { return s.raw }

// Path returns the leaf's dotted path within its document.
func (s Scalar) Path() string { return s.path }

// Resolve returns the leaf's real value, running it through the chain. A leaf no
// resolver claims comes back as its literal.
func (s Scalar) Resolve(ctx context.Context, chain Chain[string]) (string, error) {
	v, claimed, err := chain.Resolve(ctx, s.raw)
	if err != nil {
		return "", fmt.Errorf("%s:%d: %s: %w", s.path, s.line, s.raw, err)
	}
	if !claimed {
		return s.raw, nil // a static value is itself
	}
	return v, nil
}

// Document is a parsed YAML file: a tree of nodes with Scalar leaves.
type Document struct {
	path   string
	root   *yaml.Node
	exists bool
}

// Read parses the YAML file at path. A file that does not exist yields a
// Document reporting Exists() == false and no error — an absent optional file is
// the caller's business, not a parse failure.
func Read(path string) (Document, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- path derives from operator-supplied --dir/env
	if os.IsNotExist(err) {
		return Document{path: path}, nil
	}
	if err != nil {
		return Document{}, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(path, b)
}

// Parse is Read for bytes already in hand.
func Parse(path string, b []byte) (Document, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return Document{}, fmt.Errorf("parse %s: %w", path, err)
	}
	// An empty file decodes to a zero node: a valid, empty document.
	if root.Kind == 0 {
		return Document{path: path, exists: true}, nil
	}
	return Document{path: path, root: &root, exists: true}, nil
}

// Exists reports whether the file was present.
func (d Document) Exists() bool { return d.exists }

// Path returns the file's path.
func (d Document) Path() string { return d.path }

// Decode decodes the document into out with every leaf exactly as written. No
// resolver runs, so an `env:FOO` stays the literal text "env:FOO".
func (d Document) Decode(out any) error {
	if d.root == nil {
		return nil // absent or empty file: leave out at its zero value
	}
	if err := d.root.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", d.path, err)
	}
	return nil
}

// DecodeStrict is Decode, but REJECTS unknown fields: a key the target type does not
// declare is an error rather than a silently dropped value. Use it where a typo must
// fail loudly — a hand-placed host descriptor, or an older agent handed a newer schema
// it does not understand.
//
// yaml.Node.Decode cannot enforce that, so the node is re-encoded and fed to a
// KnownFields decoder. Re-encoding rather than keeping the source bytes means a
// Document never carries the file twice.
//
// A present-but-EMPTY document is an ERROR here, not a zero value: a caller that asks
// for strict decoding wants a typo to fail, and a truncated or blank file is the
// loudest typo there is. (An ABSENT file is still the caller's business — Exists()
// reports it and this returns nil, same as Decode.)
func (d Document) DecodeStrict(out any) error {
	if !d.exists {
		return nil
	}
	if d.root == nil {
		return fmt.Errorf("decode %s: empty document", d.path)
	}
	b, err := yaml.Marshal(d.root)
	if err != nil {
		return fmt.Errorf("decode %s: %w", d.path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", d.path, err)
	}
	return nil
}

// DecodeResolved decodes the document into out, resolving EVERY leaf it decodes
// through the chain first. This is the fail-fast path: a command that is about to
// touch real infrastructure resolves the whole file up front, so a missing value
// is reported before anything is created rather than part-way through.
//
// A leaf no resolver claims decodes as its literal, so a file with no patterns in
// it behaves exactly as Decode.
func (d Document) DecodeResolved(ctx context.Context, chain Chain[string], out any) error {
	if d.root == nil {
		return nil
	}
	resolved, err := resolveNode(ctx, chain, d.root, "", map[*yaml.Node]*yaml.Node{})
	if err != nil {
		return fmt.Errorf("%s: %w", d.path, err)
	}
	if err := resolved.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", d.path, err)
	}
	return nil
}

// At navigates to a leaf by its path through mapping keys and returns it. The
// second result is false when the path is absent — an unset optional field, which
// is not an error.
//
// This is what lets a command resolve ONE field: it reads the leaf it needs and
// never touches the rest of the document, so a variable it does not read can
// never fail it.
func (d Document) At(path ...string) (Scalar, bool) {
	node := deref(d.root)
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = deref(node.Content[0])
	}
	for _, key := range path {
		if node == nil || node.Kind != yaml.MappingNode {
			return Scalar{}, false
		}
		var next *yaml.Node
		// A mapping's Content alternates key, value.
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				next = deref(node.Content[i+1])
				break
			}
		}
		if next == nil {
			return Scalar{}, false
		}
		node = next
	}
	if node == nil || node.Kind != yaml.ScalarNode {
		return Scalar{}, false
	}
	return Scalar{raw: node.Value, path: strings.Join(path, "."), line: node.Line}, true
}

// deref follows an alias to its anchor, so a leaf reached through *anchor reads the
// same as one written inline.
func deref(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

// resolveNode returns a copy of node with every string leaf resolved.
//
// seen memoizes original node -> resolved copy. That is what makes ANCHORS and
// ALIASES work: an alias node carries no Content, only a pointer to its anchor, so
// a naive copy would leave that pointer aimed at the ORIGINAL, unresolved node —
// and yaml would follow it at decode time, delivering a raw `env:HCLOUD_TOKEN`
// straight to the provider with no error. Here an alias is repointed at the
// RESOLVED copy of its anchor, and the memo both keeps the two views identical and
// terminates on recursive anchors. Merge keys (`<<: *defaults`) are aliases, so
// they are covered by the same fix.
//
// Only string leaves are resolved: a pattern is text, and a resolver has nothing to
// say about an int. Mapping KEYS are carried through untouched — a reference
// belongs in a value.
func resolveNode(ctx context.Context, chain Chain[string], node *yaml.Node, path string, seen map[*yaml.Node]*yaml.Node) (*yaml.Node, error) {
	if node == nil {
		return nil, nil
	}
	if done, ok := seen[node]; ok {
		return done, nil
	}
	out := *node
	out.Content = nil
	seen[node] = &out // before recursing, so a recursive anchor terminates

	switch node.Kind {
	case yaml.AliasNode:
		target, err := resolveNode(ctx, chain, node.Alias, path, seen)
		if err != nil {
			return nil, err
		}
		out.Alias = target
		return &out, nil

	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return &out, nil // an int is an int; no resolver has anything to say about it
		}
		r, claimed := chain.Claim(node.Value)
		if !claimed {
			return &out, nil // a static value, exactly as written
		}
		v, err := r.Resolve(ctx, node.Value)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %s: %s: %w", path, node.Line, node.Value, r.Name(), err)
		}
		out.Value = v
		// The author decides the resolved type with ordinary YAML quoting. An
		// UNQUOTED reference re-infers its tag, so `port: env:PORT` yields an int and
		// `enabled: env:FLAG` a bool. A QUOTED one stays a string, so a credential
		// that happens to be all digits does not become an int.
		if node.Style == 0 {
			out.Tag = ""
		}
		return &out, nil
	}

	for i, child := range node.Content {
		childPath := path
		if node.Kind == yaml.MappingNode {
			if i%2 == 0 {
				k := *child
				out.Content = append(out.Content, &k)
				continue
			}
			childPath = join(path, node.Content[i-1].Value)
		} else if node.Kind == yaml.SequenceNode {
			childPath = fmt.Sprintf("%s[%d]", path, i)
		}
		rc, err := resolveNode(ctx, chain, child, childPath, seen)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, rc)
	}
	return &out, nil
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
