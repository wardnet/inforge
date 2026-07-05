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
// (api, container bridge) that declares API_TOKEN as a `vault:` secret, a
// sibling on the same container (web), and a global service (edge, container
// edge).
func secretFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	write("prd/regional/service/api/manifest.yaml", "name: api\ncontainer: bridge\nhost: bridge-01\ntype: raw\nuser: api\n")
	write("prd/regional/service/api/environment.yaml", "API_TOKEN: \"vault:API_TOKEN\"\n")
	write("prd/regional/service/web/manifest.yaml", "name: web\ncontainer: bridge\nhost: bridge-01\ntype: raw\nuser: web\n")
	write("prd/global/service/edge/manifest.yaml", "name: edge\ncontainer: edge\nhost: edge-01\ntype: raw\nuser: edge\n")
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

	// set --generate writes a decryptable 43-char base64url value.
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true, false))
	store, err := secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	ct, ok := store.Get("bridge", "API_TOKEN")
	require.True(t, ok, "the value must be stored under the service's container")
	plaintext, err := secretstore.Decrypt(ct, identity)
	require.NoError(t, err)
	assert.Len(t, plaintext, 43, "32 random bytes base64url without padding")

	// set --generate again: the value changes (fresh randomness, fresh nonce) —
	// replacing a leaked value is just another set.
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true, false))
	store, err = secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	ct2, _ := store.Get("bridge", "API_TOKEN")
	plaintext2, err := secretstore.Decrypt(ct2, identity)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, plaintext2)
}

func TestRunSecretWriteWithoutInitFails(t *testing.T) {
	dir := secretFixture(t)
	err := runSecretWrite(dir, "prd", "api", "API_TOKEN", true, false)
	require.ErrorContains(t, err, "inforge secret init")
}

// TestRunSecretReservedRoundTrip: --reserved writes/reads/removes a value in the
// reserved namespace without any service backing it (the observability fix), and
// it does NOT collide with a service container of the same name.
func TestRunSecretReservedRoundTrip(t *testing.T) {
	dir := secretFixture(t)
	identity, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", recipient))

	// "observability" is a reserved namespace, backed by no service — plain `set`
	// (no --reserved) would fail to resolve it as a service.
	require.NoError(t, runSecretWrite(dir, "prd", "observability", "otlp_auth", true, true))

	store, err := secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	ct, ok := store.GetReserved("observability", "otlp_auth")
	require.True(t, ok, "the value must land in the reserved namespace")
	plaintext, err := secretstore.Decrypt(ct, identity)
	require.NoError(t, err)
	assert.Len(t, plaintext, 43)
	// It is NOT in the container namespace — a service container "observability"
	// would be free of it.
	_, ok = store.Get("observability", "otlp_auth")
	assert.False(t, ok, "reserved secrets never occupy the container namespace")

	require.NoError(t, runSecretRm(dir, "prd", "observability", "otlp_auth", true))
	store, err = secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	_, ok = store.GetReserved("observability", "otlp_auth")
	assert.False(t, ok)

	err = runSecretRm(dir, "prd", "observability", "otlp_auth", true)
	require.ErrorContains(t, err, "no secret")
}

func TestRunSecretRm(t *testing.T) {
	dir := secretFixture(t)
	_, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", recipient))
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true, false))

	require.NoError(t, runSecretRm(dir, "prd", "api", "API_TOKEN", false))
	store, err := secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	_, ok := store.Get("bridge", "API_TOKEN")
	assert.False(t, ok)

	err = runSecretRm(dir, "prd", "api", "API_TOKEN", false)
	require.ErrorContains(t, err, "no secret")
}

func TestRunSecretRotate(t *testing.T) {
	dir := secretFixture(t)
	oldIdentity, oldRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", oldRecipient))
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true, false))

	// Key rotation requires the current identity.
	t.Setenv(secretstore.IdentityEnvVar, "")
	newIdentity, newRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	err = runSecretRotate(dir, "prd", newRecipient)
	require.ErrorContains(t, err, secretstore.IdentityEnvVar)

	t.Setenv(secretstore.IdentityEnvVar, oldIdentity)
	require.NoError(t, runSecretRotate(dir, "prd", newRecipient))

	store, err := secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	assert.Equal(t, newRecipient, store.Recipient)
	ct, ok := store.Get("bridge", "API_TOKEN")
	require.True(t, ok)
	// Decryptable with the NEW identity only; the plaintext survived the rotation.
	plaintext, err := secretstore.Decrypt(ct, newIdentity)
	require.NoError(t, err)
	assert.Len(t, plaintext, 43)
	_, err = secretstore.Decrypt(ct, oldIdentity)
	require.Error(t, err)
}

// TestCompromisedValueGuidance: rotate's post-rotation warning addresses every
// stored entry by a real service handle, flagging containers with none.
func TestCompromisedValueGuidance(t *testing.T) {
	dir := secretFixture(t)
	store := &secretstore.Store{Recipient: "age1test"}
	store.Set("bridge", "API_TOKEN", "ct")
	store.Set("bridge", "SESSION_KEY", "ct")
	store.Set("orphan", "TOKEN", "ct")

	lines, err := compromisedValueGuidance(dir, "prd", store)
	require.NoError(t, err)
	assert.Equal(t, []string{
		// api, not web: alphabetically-first service of the container.
		"inforge secret set prd api API_TOKEN",
		"inforge secret set prd api SESSION_KEY",
		`# container "orphan" has no declared service for key TOKEN`,
	}, lines)
}
