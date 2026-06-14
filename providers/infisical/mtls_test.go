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

// TestWriteServiceCerts asserts the imperative cert write resolves the
// deterministic per-(container, region) workspace by name and upserts the
// leaf/key/bundle under "/<service>/mtls" in the env's slug — the same location
// the bootstrapper reads.
func TestWriteServiceCerts(t *testing.T) {
	var secretWrites []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/universal-auth/login":
			_, _ = w.Write([]byte(`{"accessToken":"tok"}`))
		case r.URL.Path == "/api/v1/projects" && r.Method == http.MethodGet:
			// The workspace already exists under its deploy name.
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

	a := New("cid", "csecret", srv.URL, "", "use1")
	files := map[string]string{"leaf.crt": "LEAF", "leaf.key": "KEY", "bundle.crt": "BUNDLE"}
	require.NoError(t, a.WriteServiceCerts(context.Background(), "bridge", "bridge", "prd", "mtls", files))

	require.Len(t, secretWrites, 3)
	got := map[string]string{}
	for _, w := range secretWrites {
		assert.Equal(t, "ws-9", w["projectId"])
		assert.Equal(t, "prod", w["environment"], "prd maps to the prod infisical slug")
		assert.Equal(t, "/bridge/mtls", w["secretPath"])
		got[w["__key"].(string)] = w["secretValue"].(string)
	}
	assert.Equal(t, map[string]string{"leaf.crt": "LEAF", "leaf.key": "KEY", "bundle.crt": "BUNDLE"}, got)
}
