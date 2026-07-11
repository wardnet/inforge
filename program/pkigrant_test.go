package program

import (
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/hostsecrets"
	"github.com/wardnet/inforge/internal/types"
)

// resolveOut awaits a known StringOutput (a granted PEM is a plan-time constant).
func resolveOut(t *testing.T, out pulumi.StringOutput) string {
	t.Helper()
	ch := make(chan string, 1)
	out.ApplyT(func(s string) string { ch <- s; return s })
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("output did not resolve")
		return ""
	}
}

func pkiAll() types.AllOutputs {
	return types.AllOutputs{
		PKI: map[string]types.PKIMaterial{
			"daemon-jwt": {Cert: "CERT-PEM", Key: "KEY-PEM"},
		},
	}
}

func pkiSvc(perm string, outputs map[string]string, resource string) types.ServiceSpec {
	return types.ServiceSpec{
		Name: "tenants", Container: "tenants", Host: "tenants", Type: "raw", User: "tenants",
		Grants: []types.GrantSpec{{Resource: resource, Permission: perm, Outputs: outputs}},
	}
}

// A rw grant splits into the two halves the agent rejoins: the descriptor names
// env-var -> file key (no PEM), and the blob carries file key -> PEM. The env var
// the service reads must resolve to a PATH, never to the key material.
func TestResolvePKIGrantsSplitsDescriptorAndBlob(t *testing.T) {
	svc := pkiSvc("rw", map[string]string{
		"TENANTS_JWT_SIGNING_KEY_PATH": "{KEY}",
		"TENANTS_JWT_VERIFY_KEY_PATH":  "{CERT}",
	}, "pki/daemon-jwt")

	descFiles, blobFiles, err := resolvePKIGrants(nil, svc, pkiAll(), "prd", globalScope)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"TENANTS_JWT_SIGNING_KEY_PATH": "pki/daemon-jwt/key.pem",
		"TENANTS_JWT_VERIFY_KEY_PATH":  "pki/daemon-jwt/cert.pem",
	}, descFiles, "the descriptor names keys, never PEM content")

	require.Len(t, blobFiles, 2)
	assert.Equal(t, "KEY-PEM", resolveOut(t, blobFiles["pki/daemon-jwt/key.pem"]))
	assert.Equal(t, "CERT-PEM", resolveOut(t, blobFiles["pki/daemon-jwt/cert.pem"]))

	// The two maps join on the file key — the contract the agent's projectFiles
	// relies on (files[envVar] == key, blob.Files[key] == PEM).
	for _, key := range descFiles {
		assert.Contains(t, blobFiles, key, "every descriptor file key must have blob material")
	}
}

// A regional service reaches a global PKI as "pki/global/<name>"; the store is
// env-scoped, so it resolves the same material under the bare name.
func TestResolvePKIGrantsGlobalPrefix(t *testing.T) {
	svc := pkiSvc("ro", map[string]string{"DDNS_JWT_VERIFY_KEY_PATH": "{CERT}"}, "pki/global/daemon-jwt")

	descFiles, blobFiles, err := resolvePKIGrants(nil, svc, pkiAll(), "prd", "us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "pki/daemon-jwt/cert.pem", descFiles["DDNS_JWT_VERIFY_KEY_PATH"])
	assert.Equal(t, "CERT-PEM", resolveOut(t, blobFiles["pki/daemon-jwt/cert.pem"]))

	// A verify grant must never ship the signing key to the host.
	assert.NotContains(t, blobFiles, "pki/daemon-jwt/key.pem")
}

// A grant whose target has no material must fail the deploy. Silently skipping it
// (the pre-slice-C behaviour) shipped a descriptor missing the env vars the
// service requires, and the service crash-looped at startup.
func TestResolvePKIGrantsMissingTargetFails(t *testing.T) {
	svc := pkiSvc("rw", map[string]string{"X": "{KEY}"}, "pki/absent")
	_, _, err := resolvePKIGrants(nil, svc, pkiAll(), "prd", globalScope)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// A grantable type wired into grant.For but with no deploy-side resolver must be
// rejected, not skipped — the failure mode this whole slice exists to close.
func TestResolvePKIGrantsUnknownTypeFails(t *testing.T) {
	svc := pkiSvc("ro", map[string]string{"X": "{CERT}"}, "kafka/events")
	_, _, err := resolvePKIGrants(nil, svc, pkiAll(), "prd", globalScope)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no deploy-side resolver")
}

// Database grants are the other resolver's business: resolvePKIGrants must leave
// them alone rather than fail on them.
func TestResolvePKIGrantsIgnoresDatabaseGrants(t *testing.T) {
	svc := pkiSvc("rw", map[string]string{"DB_URL": "{URL}"}, "database/main")
	descFiles, blobFiles, err := resolvePKIGrants(nil, svc, pkiAll(), "prd", globalScope)
	require.NoError(t, err)
	assert.Empty(t, descFiles)
	assert.Empty(t, blobFiles)
}

// The descriptor merges pki-grant files with any mtls_files: material; the two key
// namespaces are disjoint ("pki/" vs "mtls/") so neither can clobber the other.
func TestRenderDescriptorMergesGrantFilesWithMtls(t *testing.T) {
	svc := types.ServiceSpec{Name: "tunneller", Host: "tunneller", Type: "raw", User: "tunneller", Pki: "wardnet-mesh", MtlsFiles: true}
	grantFiles := map[string]string{"TUNNELLER_JWT_VERIFY_KEY_PATH": "pki/daemon-jwt/cert.pem"}

	out, err := renderDescriptor(svc, types.ComputeOutputs{}, nil, grantFiles, "prd", "us-east-1", "use1", "wardnet.network", "tunneller-01", 9500)
	require.NoError(t, err)

	assert.Contains(t, out, "TUNNELLER_JWT_VERIFY_KEY_PATH: pki/daemon-jwt/cert.pem", "the grant's file entry is advertised")
	assert.Contains(t, out, "MTLS_LEAF_CERT_PATH:", "mtls material survives the merge")
	assert.NotContains(t, out, "CERT-PEM", "the descriptor must never carry PEM content")
}

// blobFrom splits the single flat await back into Env and Files by position. A
// drift here would silently write a PEM into an env var (or vice versa).
func TestBlobFromSplitsEnvAndFiles(t *testing.T) {
	names := []string{"COOKIE_KEY", "TENANTS_DATABASE_URL"}
	fileKeys := []string{"pki/daemon-jwt/cert.pem", "pki/daemon-jwt/key.pem"}
	args := []any{"cookie-value", "postgres://…", "CERT-PEM", "KEY-PEM"}

	blob, err := blobFrom(names, fileKeys, args)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"COOKIE_KEY": "cookie-value", "TENANTS_DATABASE_URL": "postgres://…"}, blob.Env)
	assert.Equal(t, map[string]string{"pki/daemon-jwt/cert.pem": "CERT-PEM", "pki/daemon-jwt/key.pem": "KEY-PEM"}, blob.Files)
}

// An empty resolved PEM would project a zero-byte file the service fails to parse
// at startup — it must abort the deploy, not reach the host.
func TestBlobFromRejectsEmptyValues(t *testing.T) {
	_, err := blobFrom(nil, []string{"pki/daemon-jwt/key.pem"}, []any{""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty resolved PEM")

	_, err = blobFrom([]string{"TOKEN"}, nil, []any{""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty resolved value")
}

// The blob a pki grant produces must round-trip through the age envelope with its
// Files map intact — the half the agent projects.
func TestBlobWithFilesRoundTrips(t *testing.T) {
	blob := hostsecrets.Blob{
		Env:   map[string]string{"COOKIE_KEY": "v"},
		Files: map[string]string{"pki/daemon-jwt/key.pem": "KEY-PEM"},
	}
	hash, err := blob.Hash()
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	// The hash covers the Files half: rotating the PEM must re-trigger the write.
	rotated := hostsecrets.Blob{
		Env:   map[string]string{"COOKIE_KEY": "v"},
		Files: map[string]string{"pki/daemon-jwt/key.pem": "KEY-PEM-2"},
	}
	rotatedHash, err := rotated.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, hash, rotatedHash, "a rotated PKI key must change the secrets.age trigger hash")
}

// A file-field template is a path, not a composable string (the
// grant-file-field-output-must-stand-alone rule).
func TestFileFieldRejectsComposedTemplate(t *testing.T) {
	_, err := fileField("path={CERT}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one {FIELD}")

	field, err := fileField("{CERT}")
	require.NoError(t, err)
	assert.Equal(t, "CERT", field)
}

// The grant target strips the cross-scope prefix to reach the env-scoped store key.
func TestPKIGrantTarget(t *testing.T) {
	assert.Equal(t, "daemon-jwt", pkiGrantTarget("daemon-jwt"))
	assert.Equal(t, "daemon-jwt", pkiGrantTarget("global/daemon-jwt"))
	assert.False(t, strings.Contains(pkiGrantTarget("global/daemon-jwt"), "/"))
}
