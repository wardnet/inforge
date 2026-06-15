package grant

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		assert.Equal(t, []string{"USER", "PASSWORD", "HOST", "PORT", "DBNAME"}, values, "perm %s", perm)
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

func TestGrantStubsNotImplemented(t *testing.T) {
	_, err := Database{}.Grant(nil, "api", PermissionRW, "prd", "us-east-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slice B")

	_, err = PKIResource{}.Grant(nil, "daemon", PermissionRW, "prd", "global")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slice C")
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
