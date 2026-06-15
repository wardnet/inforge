package resources

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrantSQLUnknownPermission(t *testing.T) {
	_, err := grantSQL("admin", "r", "db")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown grant permission")
}

func TestGrantSQLReadOnly(t *testing.T) {
	stmts, err := grantSQL("ro", "svc", "appdb")
	require.NoError(t, err)
	joined := strings.Join(stmts, "\n")

	assert.Contains(t, joined, `GRANT CONNECT ON DATABASE "appdb" TO "svc"`)
	assert.Contains(t, joined, `GRANT USAGE ON SCHEMA public TO "svc"`)
	assert.Contains(t, joined, `GRANT SELECT ON ALL TABLES IN SCHEMA public TO "svc"`)
	assert.Contains(t, joined, `ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO "svc"`)
	// ro is read-only: no write privileges and no schema-create (DDL).
	assert.NotContains(t, joined, "INSERT")
	assert.NotContains(t, joined, "UPDATE")
	assert.NotContains(t, joined, "DELETE")
	assert.NotContains(t, joined, "CREATE")
}

func TestGrantSQLReadWrite(t *testing.T) {
	stmts, err := grantSQL("rw", "svc", "appdb")
	require.NoError(t, err)
	joined := strings.Join(stmts, "\n")

	assert.Contains(t, joined, `GRANT CONNECT ON DATABASE "appdb" TO "svc"`)
	assert.Contains(t, joined, `GRANT USAGE, CREATE ON SCHEMA public TO "svc"`, "rw grants DDL (CREATE) so the service owns its migrations")
	assert.Contains(t, joined, `GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO "svc"`)
	assert.Contains(t, joined, `GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO "svc"`)
	assert.Contains(t, joined, `ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "svc"`)
}

func TestGrantSQLQuotesIdentifiers(t *testing.T) {
	// A role/db name with a double quote must be sanitized (doubled), never
	// interpolated raw — this is the SQL-injection boundary for grant material.
	stmts, err := grantSQL("ro", `ev"il`, `d"b`)
	require.NoError(t, err)
	joined := strings.Join(stmts, "\n")
	assert.Contains(t, joined, `"ev""il"`)
	assert.Contains(t, joined, `"d""b"`)
}

func TestParseConnURI(t *testing.T) {
	f, err := parseConnURI("postgresql://u:p%40ss@db.example.com:5433/appdb?sslmode=require")
	require.NoError(t, err)
	assert.Equal(t, "u", f.User)
	assert.Equal(t, "p@ss", f.Password)
	assert.Equal(t, "db.example.com", f.Host)
	assert.Equal(t, "5433", f.Port)
	assert.Equal(t, "appdb", f.DBName)

	// Port defaults to 5432 when the URI omits it.
	f, err = parseConnURI("postgresql://u:p@host/db")
	require.NoError(t, err)
	assert.Equal(t, "5432", f.Port)
}
