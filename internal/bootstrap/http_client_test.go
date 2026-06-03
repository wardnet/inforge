package bootstrap

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPKeyBrokerClientRegister(t *testing.T) {
	var gotToken, gotKey string
	var gotTTL float64
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/token", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		gotToken = payload["token"].(string)
		gotKey = payload["key"].(string)
		gotTTL = payload["ttl_seconds"].(float64)

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewHTTPKeyBrokerClient(srv.URL, "test-oidc-jwt", nil)
	err := c.Register("tok123", "age1key", "wardnet/my-repo", 300)
	require.NoError(t, err)

	assert.Equal(t, "Bearer test-oidc-jwt", gotAuth)
	assert.Equal(t, "tok123", gotToken)
	assert.Equal(t, "age1key", gotKey)
	assert.Equal(t, float64(300), gotTTL)
}

func TestHTTPKeyBrokerClientRegisterError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewHTTPKeyBrokerClient(srv.URL, "bad-token", nil)
	err := c.Register("tok", "key", "tenant", 60)
	assert.ErrorContains(t, err, "status 401")
}
