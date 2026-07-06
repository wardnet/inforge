package resources

import (
	"context"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNeonRoleDiff(t *testing.T) {
	r := &NeonRole{}
	base := NeonRoleArgs{
		ProjectId: "p", BranchId: "b", Database: "db", RoleName: "role",
		Permission: "ro", OwnerConnectionURI: "postgresql://o:pw@h/db", ApiKey: "k",
	}
	state := NeonRoleState{NeonRoleArgs: base}

	// Owner connection URI + API key drift only → no churn (they are transient).
	drift := base
	drift.OwnerConnectionURI = "postgresql://o:ROTATED@h2/db"
	drift.ApiKey = "k2"
	resp, err := r.Diff(context.Background(), infer.DiffRequest[NeonRoleArgs, NeonRoleState]{ID: "id", State: state, Inputs: drift})
	require.NoError(t, err)
	assert.False(t, resp.HasChanges, "owner URI / API key drift must not replace the role")

	// Permission change → replace.
	perm := base
	perm.Permission = "rw"
	resp, err = r.Diff(context.Background(), infer.DiffRequest[NeonRoleArgs, NeonRoleState]{ID: "id", State: state, Inputs: perm})
	require.NoError(t, err)
	require.True(t, resp.HasChanges)
	assert.Equal(t, p.UpdateReplace, resp.DetailedDiff["permission"].Kind)

	// Identity change (roleName) → replace.
	id := base
	id.RoleName = "other"
	resp, err = r.Diff(context.Background(), infer.DiffRequest[NeonRoleArgs, NeonRoleState]{ID: "id", State: state, Inputs: id})
	require.NoError(t, err)
	require.True(t, resp.HasChanges)
	assert.Equal(t, p.UpdateReplace, resp.DetailedDiff["roleName"].Kind)
}

// The grant-SQL and identifier-quoting behaviour moved to internal/pgrole (ADR-0036)
// and is tested there (internal/pgrole/pgrole_test.go).

func TestParseConnURI(t *testing.T) {
	raw := "postgresql://u:p%40ss@db.example.com:5433/appdb?sslmode=require"
	f, err := parseConnURI(raw)
	require.NoError(t, err)
	assert.Equal(t, "u", f.User)
	assert.Equal(t, "p@ss", f.Password, "discrete PASSWORD is the decoded literal")
	assert.Equal(t, "db.example.com", f.Host)
	assert.Equal(t, "5433", f.Port)
	assert.Equal(t, "appdb", f.DBName)
	assert.Equal(t, raw, f.URL, "URL is the verbatim (still-encoded) connection URI for safe DSN composition")

	// Port defaults to 5432 when the URI omits it.
	f, err = parseConnURI("postgresql://u:p@host/db")
	require.NoError(t, err)
	assert.Equal(t, "5432", f.Port)
}

// TestRedactParseErrNoLeak: a malformed connection URI must NOT echo the password
// into the returned error (it would land in deploy/CI logs).
func TestRedactParseErrNoLeak(t *testing.T) {
	// A control byte makes url.Parse fail; the raw URL carries a password.
	_, err := parseConnURI("postgresql://u:SUP3RSECRET@host/db\x7f")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SUP3RSECRET", "password must not appear in the parse error")
	assert.Contains(t, err.Error(), "url redacted")
}
