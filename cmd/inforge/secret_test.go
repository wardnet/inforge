package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/secretstore"
)

// secretFixture builds a minimal consumer resources dir: a regional service
// (api, container bridge), a sibling on the same container (web), a global
// service (edge, container edge), and a secrets spec declaring API_TOKEN as
// `source: encrypted` for container bridge.
func secretFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	write("prd/service/api.yaml", "name: api\ncontainer: bridge\nprovider: hetzner\nhost: bridge-01\ntype: raw\nuser: api\n")
	write("prd/service/web.yaml", "name: web\ncontainer: bridge\nprovider: hetzner\nhost: bridge-01\ntype: raw\nuser: web\n")
	write("prd/global/service/edge.yaml", "name: edge\ncontainer: edge\nprovider: hetzner\nhost: edge-01\ntype: raw\nuser: edge\n")
	write("prd/secrets/bridge.yaml", "name: bridge\ncontainer: bridge\nprovider: infisical\nsecrets:\n  API_TOKEN:\n    source: \"encrypted\"\n")
	return dir
}

func TestResolveServiceContainer(t *testing.T) {
	dir := secretFixture(t)

	container, siblings, err := resolveServiceContainer(dir, "prd", "api")
	require.NoError(t, err)
	assert.Equal(t, "bridge", container)
	names := make([]string, 0, len(siblings))
	for _, s := range siblings {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"api", "web"}, names, "every service sharing the container consumes its secrets")

	// A service declared only in the global slice resolves too.
	container, siblings, err = resolveServiceContainer(dir, "prd", "edge")
	require.NoError(t, err)
	assert.Equal(t, "edge", container)
	require.Len(t, siblings, 1)

	_, _, err = resolveServiceContainer(dir, "prd", "nope")
	require.ErrorContains(t, err, `service "nope" is not declared`)
	assert.Contains(t, err.Error(), "api", "the error should list known services")
}

func TestRunSecretInitAndWriteRoundTrip(t *testing.T) {
	dir := secretFixture(t)

	// init with an explicit recipient (the generate path prints the identity to
	// stdout, which a unit test shouldn't capture).
	identity, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", recipient))

	// init refuses to clobber an existing store.
	err = runSecretInit(dir, "prd", recipient)
	require.ErrorContains(t, err, "already exists")

	// rotate --generate writes a decryptable 43-char base64url value.
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true))
	store, err := secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	ct, ok := store.Get("bridge", "API_TOKEN")
	require.True(t, ok, "the value must be stored under the service's container")
	plaintext, err := secretstore.Decrypt(ct, identity)
	require.NoError(t, err)
	assert.Len(t, plaintext, 43, "32 random bytes base64url without padding")

	// rotate again: the value changes (fresh randomness, fresh nonce).
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true))
	store, err = secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	ct2, _ := store.Get("bridge", "API_TOKEN")
	plaintext2, err := secretstore.Decrypt(ct2, identity)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, plaintext2)
}

func TestRunSecretWriteWithoutInitFails(t *testing.T) {
	dir := secretFixture(t)
	err := runSecretWrite(dir, "prd", "api", "API_TOKEN", true)
	require.ErrorContains(t, err, "inforge secret init")
}

func TestRunSecretRm(t *testing.T) {
	dir := secretFixture(t)
	_, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", recipient))
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true))

	require.NoError(t, runSecretRm(dir, "prd", "api", "API_TOKEN"))
	store, err := secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	_, ok := store.Get("bridge", "API_TOKEN")
	assert.False(t, ok)

	err = runSecretRm(dir, "prd", "api", "API_TOKEN")
	require.ErrorContains(t, err, "no secret")
}

func TestRunSecretRekey(t *testing.T) {
	dir := secretFixture(t)
	oldIdentity, oldRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", oldRecipient))
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true))

	// Rekey requires the current identity.
	t.Setenv(secretstore.IdentityEnvVar, "")
	newIdentity, newRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	err = runSecretRekey(dir, "prd", newRecipient)
	require.ErrorContains(t, err, secretstore.IdentityEnvVar)

	t.Setenv(secretstore.IdentityEnvVar, oldIdentity)
	require.NoError(t, runSecretRekey(dir, "prd", newRecipient))

	store, err := secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	assert.Equal(t, newRecipient, store.Recipient)
	ct, ok := store.Get("bridge", "API_TOKEN")
	require.True(t, ok)
	// Decryptable with the NEW identity only; the plaintext survived the rekey.
	plaintext, err := secretstore.Decrypt(ct, newIdentity)
	require.NoError(t, err)
	assert.Len(t, plaintext, 43)
	_, err = secretstore.Decrypt(ct, oldIdentity)
	require.Error(t, err)
}
