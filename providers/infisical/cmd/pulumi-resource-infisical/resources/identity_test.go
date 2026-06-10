package resources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
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

func TestResolveOrgID(t *testing.T) {
	jwtWithOrg := func(org string) string {
		return "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"organizationId":"`+org+`"}`)) + ".sig"
	}

	// Explicit org wins and the token is not consulted (a token with no claim
	// would otherwise error).
	got, err := resolveOrgID("org-explicit", "not-a-jwt")
	require.NoError(t, err)
	assert.Equal(t, "org-explicit", got)

	// Empty explicit falls back to the JWT claim.
	got, err = resolveOrgID("", jwtWithOrg("org-from-jwt"))
	require.NoError(t, err)
	assert.Equal(t, "org-from-jwt", got)

	// Empty explicit and a token without the claim is the failure this knob
	// exists to fix — it must surface the orgIdFromToken error.
	noClaim := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + ".sig"
	_, err = resolveOrgID("", noClaim)
	assert.Error(t, err)
}

// TestAdoptOrCreateIdentityAdoptsExisting asserts that when an identity with the
// requested name already exists in the org, its id is returned without a create
// call (idempotent adopt).
func TestAdoptOrCreateIdentityAdoptsExisting(t *testing.T) {
	createCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/organizations/org-1/identity-memberships":
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

// TestEnsureUniversalAuthReadsExisting asserts that when universal auth is
// already configured (GET 200), the existing clientId is returned without an
// attach POST.
func TestEnsureUniversalAuthReadsExisting(t *testing.T) {
	postCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postCalled = true
		}
		_, _ = w.Write([]byte(`{"identityUniversalAuth":{"clientId":"client-xyz"}}`))
	}))
	defer srv.Close()

	cid, err := ensureUniversalAuth(context.Background(), srv.URL, "tok", "id-1")
	require.NoError(t, err)
	assert.Equal(t, "client-xyz", cid)
	assert.False(t, postCalled, "must not re-attach when universal auth already exists")
}

// TestEnsureUniversalAuthAttachesWhenAbsent asserts a 404 GET leads to an attach
// POST whose response carries the new clientId.
func TestEnsureUniversalAuthAttachesWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"identityUniversalAuth":{"clientId":"client-new"}}`))
		}
	}))
	defer srv.Close()

	cid, err := ensureUniversalAuth(context.Background(), srv.URL, "tok", "id-1")
	require.NoError(t, err)
	assert.Equal(t, "client-new", cid)
}

// TestEnsureUniversalAuthFailsLoudOnReadError asserts a non-404 read error is not
// swallowed into an attach attempt.
func TestEnsureUniversalAuthFailsLoudOnReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		t.Error("must not POST after a non-404 read error")
	}))
	defer srv.Close()

	_, err := ensureUniversalAuth(context.Background(), srv.URL, "tok", "id-1")
	require.Error(t, err)
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

// TestEnsureReadPrivilegeToleratesConflict asserts a genuine already-exists
// conflict (409) is fine and the slug is derived per secret path.
func TestEnsureReadPrivilegeToleratesConflict(t *testing.T) {
	var gotSlug string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		gotSlug, _ = body["slug"].(string)
		w.WriteHeader(http.StatusConflict) // privilege already exists
	}))
	defer srv.Close()
	require.NoError(t, ensureReadPrivilege(context.Background(), srv.URL, "tok", "ws", "id", "prod", "/ghost"))
	assert.Equal(t, "inforge-read-ghost", gotSlug, "slug must be derived per secret path, not fixed")
}

// TestEnsureReadPrivilegeFailsLoudOnBadRequest asserts a 400 (e.g. a malformed
// permission body) is a hard error, not silently swallowed — otherwise an
// identity ships without a read grant and fails only as a runtime 403.
func TestEnsureReadPrivilegeFailsLoudOnBadRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	require.Error(t, ensureReadPrivilege(context.Background(), srv.URL, "tok", "ws", "id", "prod", "/ghost"))
}

// TestEnsureProjectMembershipSkipsWhenMember asserts an existing membership (GET
// 200) creates nothing.
func TestEnsureProjectMembershipSkipsWhenMember(t *testing.T) {
	postCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postCalled = true
		}
		w.WriteHeader(http.StatusOK) // GET 200 => already a member
	}))
	defer srv.Close()
	require.NoError(t, ensureProjectMembership(context.Background(), srv.URL, "tok", "ws", "id"))
	assert.False(t, postCalled, "must not create membership when one already exists")
}

// TestEnsureProjectMembershipCreatesWhenAbsent asserts a 404 GET leads to a POST.
func TestEnsureProjectMembershipCreatesWhenAbsent(t *testing.T) {
	postCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPost:
			postCalled = true
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	require.NoError(t, ensureProjectMembership(context.Background(), srv.URL, "tok", "ws", "id"))
	assert.True(t, postCalled)
}
