package grant

import (
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/types"
)

// fakeRoleProvisioner records the inputs and returns fixed value fields.
type fakeRoleProvisioner struct {
	gotRole string
	gotPerm string
}

func (f *fakeRoleProvisioner) ProvisionRole(_ *pulumi.Context, roleName, permission string) (types.DBRoleFields, error) {
	f.gotRole = roleName
	f.gotPerm = permission
	return types.DBRoleFields{
		User:     pulumi.String("u").ToStringOutput(),
		Password: pulumi.String("p").ToStringOutput(),
		Host:     pulumi.String("h").ToStringOutput(),
		Port:     pulumi.String("5432").ToStringOutput(),
		DBName:   pulumi.String("db").ToStringOutput(),
		URL:      pulumi.String("postgresql://u:p@h:5432/db").ToStringOutput(),
	}, nil
}

func TestPermissionValid(t *testing.T) {
	assert.True(t, PermissionRO.Valid())
	assert.True(t, PermissionRW.Valid())
	assert.False(t, Permission("").Valid())
	assert.False(t, Permission("admin").Valid())
}

func TestFor(t *testing.T) {
	db, ok := For("database")
	require.True(t, ok)
	assert.IsType(t, Database{}, db)

	p, ok := For("pki")
	require.True(t, ok)
	assert.IsType(t, PKIResource{}, p)

	_, ok = For("kafka")
	assert.False(t, ok)
}

func TestDatabaseFieldNames(t *testing.T) {
	// ro and rw publish the same connection value fields; the permission changes
	// the DB user's privileges, not the delivered fields.
	for _, perm := range []Permission{PermissionRO, PermissionRW} {
		values, files := Database{}.FieldNames(perm)
		assert.Equal(t, []string{"USER", "PASSWORD", "HOST", "PORT", "DBNAME", "URL"}, values, "perm %s", perm)
		assert.Empty(t, files, "perm %s", perm)
	}
}

func TestPKIResourceFieldNames(t *testing.T) {
	roValues, roFiles := PKIResource{}.FieldNames(PermissionRO)
	assert.Empty(t, roValues)
	assert.Equal(t, []string{"CERT"}, roFiles, "verify (ro) publishes the CA cert only")

	rwValues, rwFiles := PKIResource{}.FieldNames(PermissionRW)
	assert.Empty(t, rwValues)
	assert.Equal(t, []string{"CERT", "KEY"}, rwFiles, "issue (rw) adds the signing key")
}

func TestDatabaseGrantRequiresProvisioner(t *testing.T) {
	// The zero value (validation path) has no provisioner — Grant must refuse.
	_, err := Database{}.Grant(nil, "api", PermissionRW, "prd", "us-east-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RoleProvisioner")
}

func TestDatabaseGrantMapsFields(t *testing.T) {
	fp := &fakeRoleProvisioner{}
	db := Database{RoleProvisioner: fp, RoleName: "wardnet-prd-use1-dbrole-api-main"}
	f, err := db.Grant(nil, "api", PermissionRW, "prd", "us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "wardnet-prd-use1-dbrole-api-main", fp.gotRole, "consumer-scoped role name is threaded through")
	assert.Equal(t, "rw", fp.gotPerm)
	for _, k := range []string{"USER", "PASSWORD", "HOST", "PORT", "DBNAME", "URL"} {
		_, ok := f.Values[k]
		assert.True(t, ok, "value field %s published", k)
	}
	assert.Empty(t, f.Files, "a database grant publishes no file fields")
}

// resolveOutput awaits a known StringOutput. Grant's file PEMs are plan-time
// constants (lifted from the decrypted store), so they resolve immediately.
func resolveOutput(t *testing.T, out pulumi.StringOutput) string {
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

func TestPKIResourceGrantDeliversMaterialPerPermission(t *testing.T) {
	p := PKIResource{Cert: "CERT-PEM", Key: "KEY-PEM"}

	// ro (verify) delivers the trust anchor ONLY — never the signing key.
	ro, err := p.Grant(nil, "ddns", PermissionRO, "prd", "us-east-1")
	require.NoError(t, err)
	assert.Empty(t, ro.Values, "a pki grant publishes no value fields")
	require.Contains(t, ro.Files, FieldCert)
	assert.NotContains(t, ro.Files, FieldKey, "a verify grant must never carry the root signing key")
	assert.Equal(t, "CERT-PEM", resolveOutput(t, ro.Files[FieldCert].PEM))

	// rw (issue) adds the signing key, and each field carries its OWN material.
	rw, err := p.Grant(nil, "tenants", PermissionRW, "prd", "global")
	require.NoError(t, err)
	require.Contains(t, rw.Files, FieldCert)
	require.Contains(t, rw.Files, FieldKey)
	assert.Equal(t, "CERT-PEM", resolveOutput(t, rw.Files[FieldCert].PEM))
	assert.Equal(t, "KEY-PEM", resolveOutput(t, rw.Files[FieldKey].PEM))
}

// A rw grant on a PKI with no signing key must fail the deploy, not deliver a
// cert-only set — the consumer would otherwise crash-loop on the missing env var.
func TestPKIResourceGrantRequiresMaterial(t *testing.T) {
	_, err := PKIResource{Cert: "CERT-PEM"}.Grant(nil, "tenants", PermissionRW, "prd", "global")
	require.Error(t, err)
	assert.Contains(t, err.Error(), FieldKey)

	// A ro grant on the same cert-only material is fine: KEY is not published.
	_, err = PKIResource{Cert: "CERT-PEM"}.Grant(nil, "ddns", PermissionRO, "prd", "us-east-1")
	require.NoError(t, err)

	// No cert at all fails even for verify.
	_, err = PKIResource{}.Grant(nil, "ddns", PermissionRO, "prd", "us-east-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), FieldCert)
}

// FileKey is the producer/consumer contract: the descriptor's files: value and the
// blob's Files key are the same string, and it doubles as the on-host relative
// path. It must never collide with the mesh material projected under "mtls/".
func TestFileKey(t *testing.T) {
	assert.Equal(t, "pki/daemon-jwt/cert.pem", FileKey("daemon-jwt", FieldCert))
	assert.Equal(t, "pki/daemon-jwt/key.pem", FileKey("daemon-jwt", FieldKey))
	assert.NotContains(t, FileKey("daemon-jwt", FieldCert), "mtls/")
}

func TestParseTemplateValid(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		fields     []string
		hasLiteral bool
	}{
		{"single value", "{USER}", []string{"USER"}, false},
		{"single file standalone", "{CERT}", []string{"CERT"}, false},
		{"composed connection string", "{USER}:{PASSWORD}@{HOST}:{PORT}/{DBNAME}", []string{"USER", "PASSWORD", "HOST", "PORT", "DBNAME"}, true},
		{"pure literal", "static-value", nil, true},
		{"empty", "", nil, false},
		{"leading literal then field", "prefix-{USER}", []string{"USER"}, true},
		{"repeated field", "{USER}-{USER}", []string{"USER", "USER"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tpl, err := ParseTemplate(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.fields, tpl.Fields())
			assert.Equal(t, tc.hasLiteral, tpl.HasLiteral())
		})
	}
}

func TestParseTemplateErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"unbalanced open", "{USER"},
		{"unbalanced close", "USER}"},
		{"empty placeholder", "{}"},
		{"bad name leading digit", "{1USER}"},
		{"bad name with dash", "{US-ER}"},
		{"stray close after field", "{USER}}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTemplate(tc.in)
			assert.Error(t, err)
		})
	}
}

func TestInterpolate(t *testing.T) {
	tpl, err := ParseTemplate("{USER}:{PASSWORD}@{HOST}:{PORT}/{DBNAME}")
	require.NoError(t, err)
	got, err := tpl.Interpolate(map[string]string{
		"USER": "svc", "PASSWORD": "pw", "HOST": "db.example.com", "PORT": "5432", "DBNAME": "app",
	})
	require.NoError(t, err)
	assert.Equal(t, "svc:pw@db.example.com:5432/app", got)

	// Repeated placeholders are each substituted; literal text is preserved.
	tpl, err = ParseTemplate("user={USER};u2={USER}")
	require.NoError(t, err)
	got, err = tpl.Interpolate(map[string]string{"USER": "svc"})
	require.NoError(t, err)
	assert.Equal(t, "user=svc;u2=svc", got)

	// A missing value is an error.
	tpl, err = ParseTemplate("{USER}")
	require.NoError(t, err)
	_, err = tpl.Interpolate(map[string]string{})
	assert.Error(t, err)
}
