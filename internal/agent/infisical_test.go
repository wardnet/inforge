package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func infisicalCred(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(infisicalCredential{ClientID: "cid", ClientSecret: "csec"})
	require.NoError(t, err)
	return b
}

// TestInfisicalFetchFlattensPaths drives a stub Infisical: universal-auth login,
// then a recursive raw read whose secrets are flattened to "<relpath>/<key>"
// references the descriptor env mapping resolves against.
func TestInfisicalFetchFlattensPaths(t *testing.T) {
	var sawAuth, sawFetch bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/universal-auth/login":
			sawAuth = true
			_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "tok-1"})
		case "/api/v3/secrets/raw":
			sawFetch = true
			assert.Equal(t, "Bearer tok-1", r.Header.Get("Authorization"))
			q := r.URL.Query()
			assert.Equal(t, "proj", q.Get("workspaceId"))
			assert.Equal(t, "prod", q.Get("environment"))
			assert.Equal(t, "/ghost", q.Get("secretPath"))
			assert.Equal(t, "true", q.Get("recursive"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"secrets": []map[string]string{
					{"secretKey": "DATABASE_URL", "secretValue": "pg://x", "secretPath": "/ghost/infra"},
					{"secretKey": "STRIPE_KEY", "secretValue": "sk_x", "secretPath": "/ghost/custom"},
					{"secretKey": "ROOT_LEVEL", "secretValue": "r", "secretPath": "/ghost"},
					// Sibling that merely shares a string prefix — must be skipped,
					// not mis-flattened into "ly/infra/LEAK".
					{"secretKey": "LEAK", "secretValue": "no", "secretPath": "/ghostly/infra"},
				},
			})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := Provider{Kind: "infisical", URL: srv.URL, Project: "proj", Environment: "prod", SecretPath: "/ghost"}
	f, err := newInfisicalFetcher(p, infisicalCred(t))
	require.NoError(t, err)
	f.http = srv.Client()

	got, err := f.Fetch(context.Background())
	require.NoError(t, err)
	assert.True(t, sawAuth)
	assert.True(t, sawFetch)
	assert.Equal(t, map[string]string{
		"infra/DATABASE_URL": "pg://x",
		"custom/STRIPE_KEY":  "sk_x",
		"ROOT_LEVEL":         "r",
	}, got)
	assert.NotContains(t, got, "ly/infra/LEAK", "a prefix-sharing sibling path must not be mis-flattened")
}

// TestInfisicalFetchTransientOn5xx: a 5xx from the fetch is classified transient
// so the backoff wrapper retries.
func TestInfisicalFetchTransientOn5xx(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/universal-auth/login" {
			_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "tok"})
			return
		}
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p := Provider{Kind: "infisical", URL: srv.URL, Project: "proj", Environment: "prod", SecretPath: "/ghost"}
	f, err := newInfisicalFetcher(p, infisicalCred(t))
	require.NoError(t, err)
	f.http = srv.Client()

	_, err = f.Fetch(context.Background())
	require.Error(t, err)
	assert.True(t, isTransient(err), "5xx must be transient")
}

// TestInfisicalAuthPermanentOn401: a 401 at login is permanent (fail fast).
func TestInfisicalAuthPermanentOn401(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := Provider{Kind: "infisical", URL: srv.URL, Project: "proj", Environment: "prod", SecretPath: "/ghost"}
	f, err := newInfisicalFetcher(p, infisicalCred(t))
	require.NoError(t, err)
	f.http = srv.Client()

	_, err = f.Fetch(context.Background())
	require.Error(t, err)
	assert.False(t, isTransient(err), "401 must be permanent")
}

func TestNewInfisicalFetcherValidates(t *testing.T) {
	p := Provider{Kind: "infisical", URL: "https://x", Project: "p", Environment: "e", SecretPath: "/s"}
	_, err := newInfisicalFetcher(p, []byte(`{"client_id":"","client_secret":""}`))
	assert.Error(t, err, "missing client credentials must error")

	_, err = newInfisicalFetcher(Provider{Kind: "infisical"}, infisicalCred(t))
	assert.Error(t, err, "missing provider coordinates must error")

	// A non-https provider URL must be refused (client_secret travels on it).
	httpURL := Provider{Kind: "infisical", URL: "http://insecure.example", Project: "p", Environment: "e", SecretPath: "/s"}
	_, err = newInfisicalFetcher(httpURL, infisicalCred(t))
	assert.Error(t, err, "non-https provider url must error")
}
