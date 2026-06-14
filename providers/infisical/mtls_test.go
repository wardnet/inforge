package infisical

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCertWriter asserts the imperative cert write resolves the deterministic
// per-(container, region) workspace by name and upserts the leaf/key/bundle under
// "/<service>/mtls" in the env's slug — the same location the bootstrapper reads.
func TestCertWriter(t *testing.T) {
	var secretWrites []map[string]any
	var projectLists int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/universal-auth/login":
			_, _ = w.Write([]byte(`{"accessToken":"tok"}`))
		case r.URL.Path == "/api/v1/projects" && r.Method == http.MethodGet:
			projectLists++
			_, _ = w.Write([]byte(`{"projects":[{"id":"ws-9","name":"wardnet-prd-use1-container-bridge"}]}`))
		case r.URL.Path == "/api/v2/folders":
			w.WriteHeader(http.StatusConflict) // folders already exist
		case strings.HasPrefix(r.URL.Path, "/api/v4/secrets/"):
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			body["__key"] = strings.TrimPrefix(r.URL.Path, "/api/v4/secrets/")
			secretWrites = append(secretWrites, body)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	cw, err := NewCertWriter(context.Background(), "prd", "use1", "cid", "csecret", srv.URL, "")
	require.NoError(t, err)
	files := map[string]string{"leaf.crt": "LEAF", "leaf.key": "KEY", "bundle.crt": "BUNDLE"}
	require.NoError(t, cw.Write(context.Background(), "bridge", "bridge", "mtls", files))
	// A second service in the same container reuses the cached workspace ID.
	require.NoError(t, cw.Write(context.Background(), "bridge", "web", "mtls", map[string]string{"leaf.crt": "L2"}))
	assert.Equal(t, 1, projectLists, "workspace ID is resolved once and cached for the run")

	require.Len(t, secretWrites, 4)
	for _, w := range secretWrites {
		assert.Equal(t, "ws-9", w["projectId"])
		assert.Equal(t, "prod", w["environment"], "prd maps to the prod infisical slug")
	}
	assert.Equal(t, "/bridge/mtls", secretWrites[0]["secretPath"])
	assert.Equal(t, "/web/mtls", secretWrites[3]["secretPath"])
}

// TestCertWriterAdoptOnly asserts renew refuses to create a workspace deploy has
// not provisioned (no matching project), rather than silently creating one.
func TestCertWriterAdoptOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/universal-auth/login":
			_, _ = w.Write([]byte(`{"accessToken":"tok"}`))
		case "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[]}`)) // no workspace exists yet
		default:
			t.Errorf("must not create or write: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	cw, err := NewCertWriter(context.Background(), "prd", "use1", "cid", "csecret", srv.URL, "")
	require.NoError(t, err)
	err = cw.Write(context.Background(), "bridge", "bridge", "mtls", map[string]string{"leaf.crt": "L"})
	require.ErrorContains(t, err, "inforge deploy")
}
