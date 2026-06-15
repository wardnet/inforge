package grant

import (
	"errors"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// dbValueFields are the value fields a Database Grant publishes. The connection
// material is the same for both permissions — ro and rw differ in the Postgres
// privileges granted to the per-service user, not in the fields delivered.
var dbValueFields = []string{"USER", "PASSWORD", "HOST", "PORT", "DBNAME"}

// Database is the Grantable for a managed database resource. A Grant creates a
// scoped per-service DB user and applies the ro/rw Postgres GRANTs, returning the
// connection value fields. The materialization lands in slice B of #117; this
// slice ships FieldNames so grant validation is real and testable.
type Database struct{}

// FieldNames returns the connection value fields, identical for ro and rw.
func (Database) FieldNames(Permission) (values, files []string) {
	return append([]string(nil), dbValueFields...), nil
}

// Grant is deferred to slice B of #117 (pgx owner connection + per-service
// NeonRole + ro/rw GRANTs).
func (Database) Grant(_ *pulumi.Context, _ string, _ Permission, _, _ string) (Fields, error) {
	return Fields{}, errors.New("grant: Database.Grant not yet implemented (slice B of #117)")
}
