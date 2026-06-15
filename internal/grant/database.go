package grant

import (
	"errors"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/wardnet/inforge/internal/types"
)

// dbValueFields are the value fields a Database Grant publishes. The connection
// material is the same for both permissions — ro and rw differ in the Postgres
// privileges granted to the per-service user, not in the fields delivered.
var dbValueFields = []string{"USER", "PASSWORD", "HOST", "PORT", "DBNAME"}

// Database is the Grantable for a managed database resource. A Grant mints a
// scoped per-service DB role (applying the ro/rw Postgres GRANTs) via the bound
// RoleProvisioner and returns its connection value fields.
//
// The zero value (returned by For) carries no provisioner and is used only for the
// credential-free FieldNames path; the deploy path constructs a Database with a
// RoleProvisioner and the consumer-scoped RoleName.
type Database struct {
	// RoleProvisioner mints the per-service role on the target database. nil on the
	// validation path.
	RoleProvisioner types.DBRoleProvisioner
	// RoleName is the consumer-scoped role identity the provisioner creates
	// (wardnet-<env>-<consumerSlug>-dbrole-<service>-<db>), supplied by the program
	// so two regions granting the same database never collide.
	RoleName string
}

// FieldNames returns the connection value fields, identical for ro and rw.
func (Database) FieldNames(Permission) (values, files []string) {
	return append([]string(nil), dbValueFields...), nil
}

// Grant mints the per-service role and returns its connection value fields. The
// role + GRANTs are a Pulumi resource created by the provider's provisioner; this
// maps its outputs onto the published field names.
func (d Database) Grant(ctx *pulumi.Context, _ string, perm Permission, _, _ string) (Fields, error) {
	if d.RoleProvisioner == nil {
		return Fields{}, errors.New("grant: Database.Grant requires a RoleProvisioner (deploy path only)")
	}
	if !perm.Valid() {
		return Fields{}, fmt.Errorf("grant: invalid database permission %q", perm)
	}
	f, err := d.RoleProvisioner.ProvisionRole(ctx, d.RoleName, string(perm))
	if err != nil {
		return Fields{}, err
	}
	return Fields{Values: map[string]pulumi.StringOutput{
		"USER":     f.User,
		"PASSWORD": f.Password,
		"HOST":     f.Host,
		"PORT":     f.Port,
		"DBNAME":   f.DBName,
	}}, nil
}
