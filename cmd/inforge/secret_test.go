package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/secretstore"
)

// secretFixture builds a minimal consumer resources dir: a regional service
// (api) that declares API_TOKEN as a `vault:` secret, a second service on the
// SAME container (web) that also declares API_TOKEN, and a global service
// (edge). api and web sharing container "bridge" is the whole point: their
// secrets must not be.
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
	write("prd/regional/service/web/environment.yaml", "API_TOKEN: \"vault:API_TOKEN\"\n")
	write("prd/global/service/edge/manifest.yaml", "name: edge\ncontainer: edge\nhost: edge-01\ntype: raw\nuser: edge\n")
	return dir
}

func TestRequireService(t *testing.T) {
	dir := secretFixture(t)

	svc, err := requireService(dir, "prd", "api")
	require.NoError(t, err)
	// The matched spec comes back, so the caller need not re-load the tree.
	assert.Equal(t, "api", svc.Name)
	assert.Equal(t, map[string]bool{"API_TOKEN": true}, declaredEncryptedKeys(svc))

	// A service declared only in the global slice resolves too.
	svc, err = requireService(dir, "prd", "edge")
	require.NoError(t, err)
	assert.Equal(t, "edge", svc.Name)

	_, err = requireService(dir, "prd", "nope")
	require.ErrorContains(t, err, `service "nope" is not declared`)
	assert.Contains(t, err.Error(), "api", "the error should list known services")
}

// TestSecretsAreServiceScoped is the regression this whole change exists for
// (ADR-0040): api and web share container "bridge" and both declare
// `vault:API_TOKEN`. Writing api's value must not touch web's — under the old
// container-keyed store this single `set` served BOTH services, so a rotation
// for one silently rotated the other.
func TestSecretsAreServiceScoped(t *testing.T) {
	dir := secretFixture(t)
	identity, recipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", recipient))

	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true, false))

	store, err := secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	_, ok := store.Get("api", "API_TOKEN")
	require.True(t, ok, "the value is stored under the SERVICE, not its container")
	_, ok = store.Get("web", "API_TOKEN")
	assert.False(t, ok, "a co-located service in the same container must NOT receive api's secret")
	_, ok = store.Get("bridge", "API_TOKEN")
	assert.False(t, ok, "the container is not a secret namespace any more")

	// web's own value is independent: setting it leaves api's untouched.
	require.NoError(t, runSecretWrite(dir, "prd", "web", "API_TOKEN", true, false))
	store, err = secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	apiCt, _ := store.Get("api", "API_TOKEN")
	webCt, _ := store.Get("web", "API_TOKEN")
	apiVal, err := secretstore.Decrypt(apiCt, identity)
	require.NoError(t, err)
	webVal, err := secretstore.Decrypt(webCt, identity)
	require.NoError(t, err)
	assert.NotEqual(t, apiVal, webVal, "two services sharing a container hold two independent values")
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
	ct, ok := store.Get("api", "API_TOKEN")
	require.True(t, ok, "the value must be stored under the service")
	plaintext, err := secretstore.Decrypt(ct, identity)
	require.NoError(t, err)
	assert.Len(t, plaintext, 43, "32 random bytes base64url without padding")

	// set --generate again: the value changes (fresh randomness, fresh nonce) —
	// replacing a leaked value is just another set.
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true, false))
	store, err = secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	ct2, _ := store.Get("api", "API_TOKEN")
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
// it does NOT collide with a service of the same name.
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
	// It is NOT in the service namespace — a SERVICE named "observability"
	// would be free of it.
	_, ok = store.Get("observability", "otlp_auth")
	assert.False(t, ok, "reserved secrets never occupy the service namespace")

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
	_, ok := store.Get("api", "API_TOKEN")
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

	// A reserved secret must be rotated too — missing it would orphan the
	// ciphertext to the old recipient while the store header advances.
	require.NoError(t, runSecretWrite(dir, "prd", "observability", "otlp_auth", true, true))

	t.Setenv(secretstore.IdentityEnvVar, oldIdentity)
	require.NoError(t, runSecretRotate(dir, "prd", newRecipient))

	store, err := secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	assert.Equal(t, newRecipient, store.Recipient)
	ct, ok := store.Get("api", "API_TOKEN")
	require.True(t, ok)
	// Decryptable with the NEW identity only; the plaintext survived the rotation.
	plaintext, err := secretstore.Decrypt(ct, newIdentity)
	require.NoError(t, err)
	assert.Len(t, plaintext, 43)
	_, err = secretstore.Decrypt(ct, oldIdentity)
	require.Error(t, err)

	// The reserved secret rotated alongside the service secret.
	rct, ok := store.GetReserved("observability", "otlp_auth")
	require.True(t, ok)
	_, err = secretstore.Decrypt(rct, newIdentity)
	require.NoError(t, err, "reserved secret must be re-encrypted to the new recipient")
	_, err = secretstore.Decrypt(rct, oldIdentity)
	require.Error(t, err, "reserved secret must no longer decrypt with the old identity")
}

// TestCompromisedValueGuidance: rotate's post-rotation warning addresses every
// stored entry — service secrets by their own name (the store key IS the CLI
// handle now) and reserved secrets via the --reserved reissue form.
func TestCompromisedValueGuidance(t *testing.T) {
	dir := secretFixture(t)
	store := &secretstore.Store{Recipient: "age1test"}
	store.Set("api", "API_TOKEN", "ct")
	store.Set("api", "SESSION_KEY", "ct")
	store.Set("web", "API_TOKEN", "ct")
	store.Set("ghost", "TOKEN", "ct") // stale: no such service
	store.SetReserved("observability", "otlp_auth", "ct")

	assert.Equal(t, []string{
		"inforge secret set prd api API_TOKEN",
		"inforge secret set prd api SESSION_KEY",
		// A stale entry gets a COMMENT, not a command — and NOT a `secret rm` line
		// either: the CLI refuses to address an undeclared service, so any command
		// offered here would hard-fail mid-incident. The entry is deleted by hand.
		`# ghost/TOKEN: service "ghost" is not declared — delete this entry from secrets.enc.yaml by hand`,
		// web's own copy of the same KEY is a DISTINCT value that must be reissued
		// separately — the point of service scoping.
		"inforge secret set prd web API_TOKEN",
		// reserved secrets have no service handle — reissue with --reserved.
		"inforge secret set prd observability otlp_auth --reserved",
	}, compromisedValueGuidance(dir, "prd", store))
}

// TestRunSecretRotateRekeysPKIStore is the regression the rotate/PKI coupling
// exists for: the PKI store's intermediate keys — and a root-only PKI's root key
// — are encrypted to the SAME CI recipient as the secret store, so a rotation
// that skips them leaves material only the OLD (leaked) identity can read,
// breaking every deploy and `inforge pki renew`. The two-tier cold root belongs
// to the offline root recipient and must be left exactly as it was.
func TestRunSecretRotateRekeysPKIStore(t *testing.T) {
	dir := secretFixture(t)
	oldIdentity, oldRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	rootIdentity, rootRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", oldRecipient))
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true, false))

	require.NoError(t, runPkiInit(dir, "prd", oldRecipient, rootRecipient))
	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-mesh", pki.TopologyTwoTier, ""))
	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-daemon", pki.TopologyRootOnly, "global"))
	t.Setenv(pki.RootIdentityEnvVar, rootIdentity)
	require.NoError(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "global"))
	require.NoError(t, runPkiIntermediate(dir, "prd", "wardnet-mesh", "eu-central"))

	before, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	mesh, _ := before.Get("wardnet-mesh")
	coldRootKey := mesh.Root.Key

	newIdentity, newRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	t.Setenv(secretstore.IdentityEnvVar, oldIdentity)
	require.NoError(t, runSecretRotate(dir, "prd", newRecipient))

	store, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	assert.Equal(t, newRecipient, store.Recipient)
	assert.Equal(t, rootRecipient, store.RootRecipient, "the offline root recipient is untouched")

	mesh, ok := store.Get("wardnet-mesh")
	require.True(t, ok)
	for _, scope := range []string{"global", "eu-central"} {
		mat := mesh.Intermediates[scope]
		_, err = secretstore.Decrypt(mat.Key, newIdentity)
		require.NoError(t, err, "scope %q intermediate key must decrypt with the NEW master identity", scope)
		_, err = secretstore.Decrypt(mat.Key, oldIdentity)
		require.Error(t, err, "scope %q intermediate key must no longer decrypt with the old identity", scope)
	}
	assert.Equal(t, coldRootKey, mesh.Root.Key, "the cold root is encrypted to the offline recipient — rotate must not touch it")
	_, err = secretstore.Decrypt(mesh.Root.Key, rootIdentity)
	require.NoError(t, err, "the cold root must still decrypt with the offline root identity")

	// The root-only PKI's root key IS the CI-held issuer key, so it rotates too.
	daemon, ok := store.Get("wardnet-daemon")
	require.True(t, ok)
	_, err = secretstore.Decrypt(daemon.Root.Key, newIdentity)
	require.NoError(t, err, "a root-only PKI's root key is CI-held and must be re-encrypted")
	_, err = secretstore.Decrypt(daemon.Root.Key, oldIdentity)
	require.Error(t, err)

	// Re-runnable: rotating again to the same recipient is a PKI no-op (the
	// completion path when a rotate died between the two store writes).
	t.Setenv(secretstore.IdentityEnvVar, newIdentity)
	n, err := rekeyPKIStore(dir, "prd", newIdentity, newRecipient, newRecipient)
	require.NoError(t, err)
	assert.Zero(t, n)
}

// A PKI store encrypted to a recipient other than the secret store's cannot be
// re-keyed with the current master identity — say so instead of writing a store
// whose keys nothing can decrypt.
func TestRunSecretRotateForeignPKIRecipientFails(t *testing.T) {
	dir := secretFixture(t)
	oldIdentity, oldRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	_, otherRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	_, rootRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", oldRecipient))
	require.NoError(t, runPkiInit(dir, "prd", otherRecipient, rootRecipient))

	_, newRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	t.Setenv(secretstore.IdentityEnvVar, oldIdentity)
	err = runSecretRotate(dir, "prd", newRecipient)
	require.ErrorContains(t, err, "is encrypted to recipient")

	// Nothing was written: the secret store still holds the old recipient.
	store, err := secretstore.Load(secretstore.Path(dir, "prd"))
	require.NoError(t, err)
	assert.Equal(t, oldRecipient, store.Recipient)
}

// A GENERATED master identity lives only in memory until it is printed, and the
// PKI store is re-encrypted to it BEFORE the secret store is written. If that
// second write fails and rotate returned the error alone, pki.enc.yaml would be
// encrypted to a key pair nobody holds — and the documented "re-run with
// --recipient <new>" resume would be impossible, since the operator never saw
// the identity. The failure path must print it.
func TestRunSecretRotatePrintsGeneratedIdentityWhenAWriteFails(t *testing.T) {
	if os.Geteuid() == 0 { // root ignores the mode bits this test relies on
		t.Skip("cannot make a file unwritable as root")
	}
	dir := secretFixture(t)
	oldIdentity, oldRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	_, rootRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", oldRecipient))
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true, false))
	require.NoError(t, runPkiInit(dir, "prd", oldRecipient, rootRecipient))
	require.NoError(t, runPkiAdd(dir, "prd", "wardnet-daemon", pki.TopologyRootOnly, "global"))

	// The PKI store rewrite succeeds; the secret-store rewrite cannot.
	secretPath := secretstore.Path(dir, "prd")
	require.NoError(t, os.Chmod(secretPath, 0o400))
	t.Cleanup(func() { _ = os.Chmod(secretPath, 0o600) })

	t.Setenv(secretstore.IdentityEnvVar, oldIdentity)
	// "" → rotate generates the key pair, so the identity exists only in memory.
	out, err := captureStdout(t, func() error { return runSecretRotate(dir, "prd", "") })
	require.Error(t, err)

	printed := strings.TrimSpace(out)
	require.NotEmpty(t, printed, "the generated identity must be printed when a store write fails")

	// The printed identity is the real one: it decrypts the PKI material rotate
	// already re-encrypted before the failure.
	store, err := pki.Load(pki.Path(dir, "prd"))
	require.NoError(t, err)
	daemon, ok := store.Get("wardnet-daemon")
	require.True(t, ok)
	_, err = secretstore.Decrypt(daemon.Root.Key, printed)
	require.NoError(t, err, "the printed identity must decrypt the already-rotated PKI key")

	// And the rotation is resumable with it: the secret store is still on the old
	// recipient, so re-running with the old identity + the new recipient finishes.
	require.NoError(t, os.Chmod(secretPath, 0o600))
	require.NoError(t, runSecretRotate(dir, "prd", store.Recipient))
	secrets, err := secretstore.Load(secretPath)
	require.NoError(t, err)
	assert.Equal(t, store.Recipient, secrets.Recipient)
	ciphertext, ok := secrets.Get("api", "API_TOKEN")
	require.True(t, ok)
	_, err = secretstore.Decrypt(ciphertext, printed)
	require.NoError(t, err, "after the resume, the secret store decrypts with the new identity")
}

// No PKI store at all (an env that never ran `inforge pki init`) is not an
// error — rotate stays a secret-store-only operation there.
func TestRunSecretRotateWithoutPKIStore(t *testing.T) {
	dir := secretFixture(t)
	oldIdentity, oldRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	require.NoError(t, runSecretInit(dir, "prd", oldRecipient))
	require.NoError(t, runSecretWrite(dir, "prd", "api", "API_TOKEN", true, false))

	_, newRecipient, err := secretstore.GenerateIdentity()
	require.NoError(t, err)
	t.Setenv(secretstore.IdentityEnvVar, oldIdentity)
	require.NoError(t, runSecretRotate(dir, "prd", newRecipient))
}
