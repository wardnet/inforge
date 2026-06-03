package bootstrap

import (
	"strings"
	"testing"

	"github.com/getsops/sops/v3/decrypt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestMint(t *testing.T) {
	m, err := Mint()
	require.NoError(t, err)
	require.NotNil(t, m.Identity)
	assert.True(t, strings.HasPrefix(m.Recipient, "age1"), "recipient should be an age public key")
	assert.Len(t, m.Token, 64, "token should be 32 bytes hex-encoded")

	other, err := Mint()
	require.NoError(t, err)
	assert.NotEqual(t, m.Token, other.Token, "tokens must be unique")
}

func TestEncryptYAMLOnlyEncryptsMatchingKeys(t *testing.T) {
	m, err := Mint()
	require.NoError(t, err)

	plain := []byte("password: hunter2\nlog_level: info\n")
	enc, err := EncryptYAML(plain, m.Recipient, "^(password)$")
	require.NoError(t, err)

	assert.Contains(t, string(enc), "ENC[")
	assert.Contains(t, string(enc), "log_level: info", "non-matching keys stay plaintext")
	assert.NotContains(t, string(enc), "hunter2", "matching key must be encrypted")

	t.Setenv("SOPS_AGE_KEY", m.Identity.String())
	dec, err := decrypt.Data(enc, "yaml")
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, yaml.Unmarshal(dec, &out))
	assert.Equal(t, "hunter2", out["password"])
}

// fakeBroker records what was registered.
type fakeBroker struct {
	token, key, tenant string
	ttlSeconds         int
	called             bool
}

func (f *fakeBroker) Register(token, key, tenant string, ttlSeconds int) error {
	f.token, f.key, f.tenant, f.ttlSeconds, f.called = token, key, tenant, ttlSeconds, true
	return nil
}

func TestRegisterBuildsBootstrapDoc(t *testing.T) {
	m, err := Mint()
	require.NoError(t, err)

	fe := &fakeBroker{}
	doc, err := Register(fe, "https://broker.example", "wardnet/inforge", m, 600)
	require.NoError(t, err)

	require.True(t, fe.called)
	assert.Equal(t, m.Token, fe.token)
	assert.Equal(t, m.Identity.String(), fe.key, "the age identity K is what is registered with the key broker")
	assert.Equal(t, "wardnet/inforge", fe.tenant)
	assert.Equal(t, 600, fe.ttlSeconds)

	assert.Equal(t, "https://broker.example", doc.BrokerURL)
	assert.Equal(t, m.Token, doc.Token)
	assert.Equal(t, "wardnet/inforge", doc.Tenant)

	// bootstrap.yaml carries the redemption coordinates but never K itself.
	b, err := doc.Marshal()
	require.NoError(t, err)
	out := string(b)
	assert.Contains(t, out, "broker_url: https://broker.example")
	assert.Contains(t, out, "token: "+m.Token)
	assert.Contains(t, out, "tenant: wardnet/inforge")
	assert.NotContains(t, out, m.Identity.String(), "K must not appear in bootstrap.yaml")
}
