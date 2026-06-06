package resources

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedReq struct {
	method string
	path   string
	body   map[string]any
}

// TestUpsertSecretIncludesSecretPath asserts the create POST carries the
// secretPath (and projectId/environment/secretValue) so a per-service write
// lands under /<svc>/infra rather than the workspace root.
func TestUpsertSecretIncludesSecretPath(t *testing.T) {
	var got capturedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = capturedReq{method: r.Method, path: r.URL.Path}
		_ = json.Unmarshal(b, &got.body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := upsertSecret(context.Background(), srv.URL, "tok", "ws-1", "prod", "/ghost/infra", "DATABASE_URL", "postgres://x")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/api/v4/secrets/DATABASE_URL", got.path)
	assert.Equal(t, "/ghost/infra", got.body["secretPath"])
	assert.Equal(t, "ws-1", got.body["projectId"])
	assert.Equal(t, "prod", got.body["environment"])
	assert.Equal(t, "postgres://x", got.body["secretValue"])
}

// TestUpsertSecretPatchOnConflict asserts a 400 on create falls back to PATCH,
// and the PATCH body also carries secretPath.
func TestUpsertSecretPatchOnConflict(t *testing.T) {
	var reqs []capturedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := capturedReq{method: r.Method, path: r.URL.Path}
		_ = json.Unmarshal(b, &c.body)
		reqs = append(reqs, c)
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := upsertSecret(context.Background(), srv.URL, "tok", "ws-1", "prod", "/ghost/infra", "KEY", "v")
	require.NoError(t, err)

	require.Len(t, reqs, 2)
	assert.Equal(t, http.MethodPost, reqs[0].method)
	assert.Equal(t, http.MethodPatch, reqs[1].method)
	assert.Equal(t, "/ghost/infra", reqs[1].body["secretPath"])
}

// TestUpsertSecretEmptyPathDefaultsToRoot keeps the historical root behaviour
// for callers that pass no path.
func TestUpsertSecretEmptyPathDefaultsToRoot(t *testing.T) {
	var got capturedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got.body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	require.NoError(t, upsertSecret(context.Background(), srv.URL, "tok", "ws", "prod", "", "K", "v"))
	assert.Equal(t, "/", got.body["secretPath"])
}

// TestEnsureFolderPathCreatesEachLevel asserts each path segment is created in
// order under its parent, and that conflicts (already-exists) are tolerated.
func TestEnsureFolderPathCreatesEachLevel(t *testing.T) {
	var reqs []capturedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := capturedReq{method: r.Method, path: r.URL.Path}
		_ = json.Unmarshal(b, &c.body)
		reqs = append(reqs, c)
		w.WriteHeader(http.StatusConflict) // pretend both already exist
	}))
	defer srv.Close()

	err := ensureFolderPath(context.Background(), srv.URL, "tok", "ws", "prod", "/ghost/infra")
	require.NoError(t, err)

	require.Len(t, reqs, 2)
	assert.Equal(t, "/api/v2/folders", reqs[0].path)
	assert.Equal(t, "ghost", reqs[0].body["name"])
	assert.Equal(t, "/", reqs[0].body["path"])
	assert.Equal(t, "infra", reqs[1].body["name"])
	assert.Equal(t, "/ghost", reqs[1].body["path"])
}

func TestEnsureFolderPathRootIsNoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no folder call expected for root path")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	require.NoError(t, ensureFolderPath(context.Background(), srv.URL, "tok", "ws", "prod", "/"))
	require.NoError(t, ensureFolderPath(context.Background(), srv.URL, "tok", "ws", "prod", ""))
}
