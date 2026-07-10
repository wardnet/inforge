package program

import (
	"reflect"
	"strings"
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
// an Export call in program.Run must therefore carry a non-empty `pulumi` tag.
//
// The tag's NAME matters too, and is asserted here as a second invariant: the
// read side (cmd/inforge's decodeTargets) unmarshals the exported JSON via
// encoding/json, which matches on the `json:` tag — so if `pulumi:"hostDns"`
// (idiomatic Pulumi camelCase) diverges from `json:"host_dns"`, the export
// itself succeeds but every read decodes that field back to its zero value.
// This exact mismatch shipped once (HostDNS et al. exported under "hostDns"
// but decoded looking for "host_dns"), and broke `inforge releases deploy`
// with an empty SSH hostname even after the empty-export bug was fixed.
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
			pulumiTag := f.Tag.Get("pulumi")
			assert.NotEmpty(t, pulumiTag, "%s.%s has no `pulumi:\"...\"` struct tag — pulumi.Any() silently drops it from the export", typ.Name(), f.Name)

			jsonTag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			assert.Equal(t, jsonTag, pulumiTag,
				"%s.%s: pulumi tag %q must match json tag %q — decodeTargets unmarshals via json, so a mismatch silently zeroes this field on read even though the export itself succeeds",
				typ.Name(), f.Name, pulumiTag, jsonTag)
		}
	}
}
