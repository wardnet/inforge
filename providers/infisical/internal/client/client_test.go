package client

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jwtWithClaims(t *testing.T, payload string) string {
	t.Helper()
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

func TestOrgIDFromToken(t *testing.T) {
	got, err := orgIDFromToken(jwtWithClaims(t, `{"organizationId":"org-abc"}`))
	require.NoError(t, err)
	assert.Equal(t, "org-abc", got)

	_, err = orgIDFromToken("not-a-jwt")
	assert.Error(t, err)
}

func TestResolveOrgID(t *testing.T) {
	// Explicit org wins and the token is not consulted (a token with no claim
	// would otherwise error).
	c := New("https://example.com")
	c.SetToken("not-a-jwt")
	got, err := c.resolveOrgID("org-explicit")
	require.NoError(t, err)
	assert.Equal(t, "org-explicit", got)

	// Empty explicit falls back to the JWT claim.
	c.SetToken(jwtWithClaims(t, `{"organizationId":"org-from-jwt"}`))
	got, err = c.resolveOrgID("")
	require.NoError(t, err)
	assert.Equal(t, "org-from-jwt", got)

	// Empty explicit and a token without the claim is the failure this knob
	// exists to fix — it must surface the orgIDFromToken error.
	c.SetToken(jwtWithClaims(t, `{}`))
	_, err = c.resolveOrgID("")
	assert.Error(t, err)
}

// TestRequireSecureURL: https is always allowed; http only for a loopback host,
// matched exactly (not by substring, so "localhost.evil.com" is rejected).
func TestRequireSecureURL(t *testing.T) {
	for _, ok := range []string{
		"https://app.infisical.com",
		"https://localhost:8080",
		"http://localhost:8080",
		"http://127.0.0.1:8080",
		"http://[::1]:8080",
	} {
		assert.NoError(t, requireSecureURL(ok), ok)
	}
	for _, bad := range []string{
		"http://app.infisical.com",
		"http://localhost.evil.com",
		"http://127.0.0.1.evil.com",
	} {
		assert.Error(t, requireSecureURL(bad), bad)
	}
}

// TestAuthenticateStoresToken exercises the UA login happy path against a
// loopback server (which the https guard permits) and asserts the token is
// stored on the Client.
func TestAuthenticateStoresToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/auth/universal-auth/login", r.URL.Path)
		_, _ = w.Write([]byte(`{"accessToken":"tok-123"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	require.NoError(t, c.Authenticate(context.Background(), "cid", "csecret"))
	assert.Equal(t, "tok-123", c.Token())
}
