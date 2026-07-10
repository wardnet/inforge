package program

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wardnet/inforge/internal/app"
	"github.com/wardnet/inforge/internal/dbbackup"
	"github.com/wardnet/inforge/internal/meshplan"
	"github.com/wardnet/inforge/internal/service"
)

// TestDeployDescriptorsCarryPulumiTags guards a class of bug that once shipped
// all four *DeployDescriptor stack outputs (deployDescriptor/appDeployDescriptor/
// meshDeployDescriptor/dbDeployDescriptor) as an empty {} in production: each is
// exported via ctx.Export(name, pulumi.Any(desc)), and the Pulumi Go SDK's
// reflection-based struct marshaler (marshalInputOptionsImpl, go/pulumi/rpc.go)
// silently DROPS any exported struct field that has no `pulumi:"..."` tag —
// yaml/json tags alone are not enough. Every field of every type reachable from
// an Export call in program.Run must therefore carry a non-empty `pulumi` tag;
// this test asserts that mechanically so a new field (or a new descriptor type)
// can't reintroduce the silent-empty-export failure mode.
func TestDeployDescriptorsCarryPulumiTags(t *testing.T) {
	types := []any{
		service.DeployDescriptor{}, service.DeployTarget{},
		app.DeployDescriptor{}, app.DeployTarget{},
		meshplan.DeployDescriptor{}, meshplan.DeployTarget{},
		dbbackup.DeployDescriptor{}, dbbackup.DeployTarget{},
	}
	for _, v := range types {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := f.Tag.Get("pulumi")
			assert.NotEmpty(t, tag, "%s.%s has no `pulumi:\"...\"` struct tag — pulumi.Any() silently drops it from the export", typ.Name(), f.Name)
		}
	}
}
