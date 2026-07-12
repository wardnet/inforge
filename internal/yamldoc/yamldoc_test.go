package yamldoc

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(pairs map[string]string) Chain {
	return Chain{EnvFrom(func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	})}
}

const doc = `base_domain: example.com
ssh:
  authorizedKeys: ${KEYS}
port: 8080
enabled: true
regions:
  - ${REGION_A}
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
	assert.Equal(t, "${KEYS}", out.SSH.AuthorizedKeys, "a literal decode leaves the pattern as written")
}

// This is why the reader needs no opt-out, no lenient mode and no exceptions
// list. A file whose leaves nobody resolves comes back verbatim — which is what
// makes it safe to read an encrypted store, or a Grafana dashboard carrying
// Grafana's OWN ${DS_FOO} template syntax, through the same reader as everything
// else. Nothing asked for a resolved value, so nothing was resolved.
func TestUnresolvedDocumentSurvivesVerbatim(t *testing.T) {
	d, err := Parse("dashboard.yaml", []byte("datasource: ${DS_PROMETHEUS}\ntitle: Infra\n"))
	require.NoError(t, err)

	var out map[string]string
	require.NoError(t, d.Decode(&out))
	assert.Equal(t, "${DS_PROMETHEUS}", out["datasource"],
		"a dashboard's own ${} template syntax must pass through untouched")
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
	// pattern, so no resolver claims it. Meanwhile ${KEYS} elsewhere is unresolvable.
	got, err := leaf.Resolve(context.Background(), env(nil))
	require.NoError(t, err)
	assert.Equal(t, "example.com", got)

	nested, ok := d.At("ssh", "authorizedKeys")
	require.True(t, ok)
	assert.Equal(t, "${KEYS}", nested.Literal(), "the literal is the text as written")
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
	chain := Chain{prefixResolver{"vault:"}, prefixResolver{"ref:"}}

	got, err := chain.Resolve(context.Background(), "vault:STRIPE_KEY")
	require.NoError(t, err)
	assert.Equal(t, "resolved-by-vault:-STRIPE_KEY", got)

	got, err = chain.Resolve(context.Background(), "ref:database/main.url")
	require.NoError(t, err)
	assert.Equal(t, "resolved-by-ref:-database/main.url", got)

	// No resolver claims it → it is a value, not a reference.
	got, err = chain.Resolve(context.Background(), "just-a-string")
	require.NoError(t, err)
	assert.Equal(t, "just-a-string", got)
}

// A resolver's failure is reported with its name, so an operator knows which
// mechanism could not produce the value.
func TestChainNamesTheFailingResolver(t *testing.T) {
	_, err := Chain{EnvFrom(func(string) (string, bool) { return "", false })}.
		Resolve(context.Background(), "${NOPE}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env:")
}

// An env var set to the empty string is treated as absent: a blank credential is
// never a legitimate value, and passing "" to a provider fails far from the cause.
func TestEnvEmptyIsMissing(t *testing.T) {
	_, err := env(map[string]string{"BLANK": ""}).Resolve(context.Background(), "${BLANK}")
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
