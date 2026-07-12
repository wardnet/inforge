package yamldoc

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(pairs map[string]string) Chain[string] {
	return Chain[string]{EnvFrom(func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	})}
}

const doc = `base_domain: example.com
ssh:
  authorizedKeys: env:KEYS
port: 8080
enabled: true
regions:
  - env:REGION_A
  - literal
`

func parse(t *testing.T) Document {
	t.Helper()
	d, err := Parse("variables.yaml", []byte(doc))
	require.NoError(t, err)
	return d
}

// Reading resolves nothing. A document with unresolvable patterns in it loads
// fine — resolution is a separate act, performed only by a consumer that wants a
// real value.
func TestReadNeverResolves(t *testing.T) {
	d := parse(t)
	var out struct {
		BaseDomain string `yaml:"base_domain"`
		SSH        struct {
			AuthorizedKeys string `yaml:"authorizedKeys"`
		} `yaml:"ssh"`
	}
	require.NoError(t, d.Decode(&out))
	assert.Equal(t, "example.com", out.BaseDomain)
	assert.Equal(t, "env:KEYS", out.SSH.AuthorizedKeys, "a literal decode leaves the pattern as written")
}

// This is why the reader needs no opt-out, no lenient mode and no exceptions
// list. A file whose leaves nobody resolves comes back verbatim — which is what
// makes it safe to read an encrypted store, or a Grafana dashboard carrying
// Grafana's OWN env:DS_FOO template syntax, through the same reader as everything
// else. Nothing asked for a resolved value, so nothing was resolved.
func TestUnresolvedDocumentSurvivesVerbatim(t *testing.T) {
	// Grafana's OWN template syntax is ${var}. Under the one DSL it is claimed by no
	// resolver, so it is a static value — and even a chain that resolves everything
	// leaves it alone. A dashboard reads through the same reader as everything else.
	d, err := Parse("dashboard.yaml", []byte("datasource: ${DS_PROMETHEUS}\ntitle: Infra\n"))
	require.NoError(t, err)

	var out map[string]string
	require.NoError(t, d.Decode(&out))
	assert.Equal(t, "${DS_PROMETHEUS}", out["datasource"])

	// ...and with a full chain, still untouched: no resolver claims it.
	out = nil
	require.NoError(t, d.DecodeResolved(context.Background(), env(nil), &out))
	assert.Equal(t, "${DS_PROMETHEUS}", out["datasource"],
		"a foreign ${} template must never be claimed by our env: resolver")
}

// The whole-file resolved decode: every leaf goes through the chain. This is the
// fail-fast path a command takes before it touches real infrastructure.
func TestDecodeResolvedResolvesEveryLeaf(t *testing.T) {
	d := parse(t)
	var out struct {
		BaseDomain string `yaml:"base_domain"`
		SSH        struct {
			AuthorizedKeys string `yaml:"authorizedKeys"`
		} `yaml:"ssh"`
		Port    int      `yaml:"port"`
		Enabled bool     `yaml:"enabled"`
		Regions []string `yaml:"regions"`
	}
	chain := env(map[string]string{"KEYS": "ssh-ed25519 AAAA", "REGION_A": "euc"})
	require.NoError(t, d.DecodeResolved(context.Background(), chain, &out))

	assert.Equal(t, "ssh-ed25519 AAAA", out.SSH.AuthorizedKeys)
	assert.Equal(t, []string{"euc", "literal"}, out.Regions, "patterns resolve inside sequences")
	// Non-string leaves keep their type: resolving must not turn an int into a
	// string or mangle a bool.
	assert.Equal(t, 8080, out.Port)
	assert.True(t, out.Enabled)
	// A leaf with no pattern is claimed by no resolver and comes back as written.
	assert.Equal(t, "example.com", out.BaseDomain)
}

// A missing value names the LEAF, not just the variable, so an operator is told
// which line of which file wanted it.
func TestDecodeResolvedNamesTheLeaf(t *testing.T) {
	d := parse(t)
	var out map[string]any
	err := d.DecodeResolved(context.Background(), env(nil), &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "variables.yaml")
	assert.Contains(t, err.Error(), "ssh.authorizedKeys")
	assert.Contains(t, err.Error(), "missing required env var: KEYS")
}

// One leaf, resolved on demand — and reading it cannot be failed by a pattern in
// a leaf it never touches.
func TestAtResolvesOneLeaf(t *testing.T) {
	d := parse(t)
	leaf, ok := d.At("base_domain")
	require.True(t, ok)

	// The chain resolves nothing at all, yet this succeeds: base_domain carries no
	// pattern, so no resolver claims it. Meanwhile env:KEYS elsewhere is unresolvable.
	got, err := leaf.Resolve(context.Background(), env(nil))
	require.NoError(t, err)
	assert.Equal(t, "example.com", got)

	nested, ok := d.At("ssh", "authorizedKeys")
	require.True(t, ok)
	assert.Equal(t, "env:KEYS", nested.Literal(), "the literal is the text as written")
	_, err = nested.Resolve(context.Background(), env(nil))
	require.Error(t, err, "asking to resolve THIS leaf does fail — it is the one that needs the variable")
}

func TestAtAbsentPath(t *testing.T) {
	d := parse(t)
	_, ok := d.At("nope")
	assert.False(t, ok)
	_, ok = d.At("ssh", "nope")
	assert.False(t, ok)
	_, ok = d.At("ssh") // a mapping, not a leaf
	assert.False(t, ok)
}

// An absent file is not a parse error — whether it is required is the caller's
// business.
func TestReadMissingFile(t *testing.T) {
	d, err := Read(filepath.Join(t.TempDir(), "nope.yaml"))
	require.NoError(t, err)
	assert.False(t, d.Exists())

	var out map[string]any
	require.NoError(t, d.Decode(&out))
	assert.Empty(t, out)
}

// The chain is ordered and open: the first resolver whose pattern matches wins,
// and a leaf no resolver claims is a static literal. Nothing about the reader
// knows what a pattern looks like — which is what lets environment.yaml's
// env:/vault:/ref: sources, or an API-backed resolver, join the chain later.
func TestChainFirstMatchWinsAndUnclaimedIsLiteral(t *testing.T) {
	chain := Chain[string]{prefixResolver{"vault:"}, prefixResolver{"ref:"}}

	got, claimed, err := chain.Resolve(context.Background(), "vault:STRIPE_KEY")
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.Equal(t, "resolved-by-vault:-STRIPE_KEY", got)

	got, claimed, err = chain.Resolve(context.Background(), "ref:database/main.url")
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.Equal(t, "resolved-by-ref:-database/main.url", got)

	// No resolver claims it → it is a value, not a reference.
	_, claimed, err = chain.Resolve(context.Background(), "just-a-string")
	require.NoError(t, err)
	assert.False(t, claimed, "an unclaimed leaf is a static literal")
}

// A resolver's failure is reported with its name, so an operator knows which
// mechanism could not produce the value.
func TestChainNamesTheFailingResolver(t *testing.T) {
	_, _, err := Chain[string]{EnvFrom(func(string) (string, bool) { return "", false })}.
		Resolve(context.Background(), "env:NOPE")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env:")
}

// An env var set to the empty string is treated as absent: a blank credential is
// never a legitimate value, and passing "" to a provider fails far from the cause.
func TestEnvEmptyIsMissing(t *testing.T) {
	_, _, err := env(map[string]string{"BLANK": ""}).Resolve(context.Background(), "env:BLANK")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required env var")
}

type prefixResolver struct{ prefix string }

func (p prefixResolver) Name() string { return p.prefix }
func (p prefixResolver) Matches(raw string) bool {
	return len(raw) > len(p.prefix) && raw[:len(p.prefix)] == p.prefix
}
func (p prefixResolver) Resolve(_ context.Context, raw string) (string, error) {
	return fmt.Sprintf("resolved-by-%s-%s", p.prefix, raw[len(p.prefix):]), nil
}

// ANCHORS AND ALIASES. An alias node carries no content, only a pointer to its
// anchor — so a naive copy leaves that pointer aimed at the ORIGINAL, unresolved
// node, and yaml follows it at decode time. That shipped a literal
// "env:HCLOUD_TOKEN" to the provider with no error at all: the exact hazard this
// design exists to prevent. Merge keys (<<:) are aliases too.
func TestAnchorsAndMergeKeysResolve(t *testing.T) {
	d, err := Parse("regions.yaml", []byte(`defaults: &d
  apiToken: env:HCLOUD_TOKEN
regions:
  euc:
    providers:
      hetzner: *d
  use1:
    providers:
      hetzner:
        <<: *d
        location: fsn1
`))
	require.NoError(t, err)

	var out struct {
		Regions map[string]struct {
			Providers map[string]map[string]string `yaml:"providers"`
		} `yaml:"regions"`
	}
	chain := env(map[string]string{"HCLOUD_TOKEN": "REAL-TOKEN"})
	require.NoError(t, d.DecodeResolved(context.Background(), chain, &out))

	assert.Equal(t, "REAL-TOKEN", out.Regions["euc"].Providers["hetzner"]["apiToken"],
		"an aliased credential must be resolved, not passed through as a literal")
	assert.Equal(t, "REAL-TOKEN", out.Regions["use1"].Providers["hetzner"]["apiToken"],
		"a merge-key credential must be resolved too")
	assert.Equal(t, "fsn1", out.Regions["use1"].Providers["hetzner"]["location"])
}

// An unresolvable reference behind an anchor still FAILS — it must not quietly
// become a literal.
func TestAnchoredReferenceStillFailsWhenUnset(t *testing.T) {
	d, err := Parse("regions.yaml", []byte("defaults: &d\n  apiToken: env:NOPE\nuse: *d\n"))
	require.NoError(t, err)
	var out map[string]any
	err = d.DecodeResolved(context.Background(), env(nil), &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required env var: NOPE")
}

// A leaf reached THROUGH an alias reads the same as one written inline.
func TestAtFollowsAliases(t *testing.T) {
	d, err := Parse("v.yaml", []byte("anchor: &a\n  base_domain: example.com\nalias: *a\n"))
	require.NoError(t, err)
	leaf, ok := d.At("alias", "base_domain")
	require.True(t, ok)
	assert.Equal(t, "example.com", leaf.Literal())
}

// The author decides the resolved type with ordinary YAML quoting: an UNQUOTED
// reference re-infers its tag, a QUOTED one stays a string. Without this, every
// resolved value would be a string and `port: env:PORT` could not decode into an
// int — and with only this, a credential that happens to be all digits would
// silently become one.
func TestQuotingDecidesTheResolvedType(t *testing.T) {
	d, err := Parse("v.yaml", []byte("port: env:PORT\nenabled: env:FLAG\ntoken: \"env:TOKEN\"\n"))
	require.NoError(t, err)
	var out struct {
		Port    int    `yaml:"port"`
		Enabled bool   `yaml:"enabled"`
		Token   string `yaml:"token"`
	}
	chain := env(map[string]string{"PORT": "8080", "FLAG": "true", "TOKEN": "1698762"})
	require.NoError(t, d.DecodeResolved(context.Background(), chain, &out))

	assert.Equal(t, 8080, out.Port, "an unquoted reference re-infers its type")
	assert.True(t, out.Enabled)
	assert.Equal(t, "1698762", out.Token, "a quoted reference stays a string, digits and all")
}
