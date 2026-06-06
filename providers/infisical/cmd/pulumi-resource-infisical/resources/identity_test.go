package resources

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrgIdFromToken(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"organizationId":"org-abc"}`))
	token := "header." + payload + ".sig"
	got, err := orgIdFromToken(token)
	require.NoError(t, err)
	assert.Equal(t, "org-abc", got)

	_, err = orgIdFromToken("not-a-jwt")
	assert.Error(t, err)
}

// TestAdoptOrCreateIdentityAdoptsExisting asserts that when an identity with the
// requested name already exists in the org, its id is returned without a create
// call (idempotent adopt).
func TestAdoptOrCreateIdentityAdoptsExisting(t *testing.T) {
	createCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations/org-1/identity-memberships":
			_, _ = w.Write([]byte(`{"identityMemberships":[{"identity":{"id":"id-existing","name":"wardnet-prd-use1-identity-ghost"}}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/identities":
			createCalled = true
			_, _ = w.Write([]byte(`{"identity":{"id":"id-new"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	id, err := adoptOrCreateIdentity(context.Background(), srv.URL, "tok", "org-1", "wardnet-prd-use1-identity-ghost")
	require.NoError(t, err)
	assert.Equal(t, "id-existing", id)
	assert.False(t, createCalled, "create must not be called when the identity already exists")
}

// TestAdoptOrCreateIdentityCreatesWhenAbsent asserts a create when no identity
// matches the requested name.
func TestAdoptOrCreateIdentityCreatesWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"identityMemberships":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/identities":
			_, _ = w.Write([]byte(`{"identity":{"id":"id-new"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	id, err := adoptOrCreateIdentity(context.Background(), srv.URL, "tok", "org-1", "ghost")
	require.NoError(t, err)
	assert.Equal(t, "id-new", id)
}

// TestEnsureUniversalAuthReadsExistingOnConflict asserts that when attach
// conflicts (already configured), the existing clientId is fetched via GET.
func TestEnsureUniversalAuthReadsExistingOnConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusBadRequest) // already attached
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"identityUniversalAuth":{"clientId":"client-xyz"}}`))
		}
	}))
	defer srv.Close()

	cid, err := ensureUniversalAuth(context.Background(), srv.URL, "tok", "id-1")
	require.NoError(t, err)
	assert.Equal(t, "client-xyz", cid)
}

func TestMintClientSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/auth/universal-auth/identities/id-1/client-secrets", r.URL.Path)
		_, _ = w.Write([]byte(`{"clientSecret":"secret-value"}`))
	}))
	defer srv.Close()

	cs, err := mintClientSecret(context.Background(), srv.URL, "tok", "id-1")
	require.NoError(t, err)
	assert.Equal(t, "secret-value", cs)
}

func TestEnsureReadPrivilegeTolerient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict) // privilege already exists
	}))
	defer srv.Close()
	require.NoError(t, ensureReadPrivilege(context.Background(), srv.URL, "tok", "ws", "id", "prod", "/ghost"))
}
