package cloudflare

import (
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
)

// recordMocks captures the tag count passed to each created DNS record so the
// tagRecords toggle can be asserted end-to-end through Create.
type recordMocks struct {
	mu       sync.Mutex
	tagCount []int
}

func (m *recordMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *recordMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == "cloudflare:index/record:Record" {
		n := 0
		if v, ok := args.Inputs["tags"]; ok && v.IsArray() {
			n = len(v.ArrayValue())
		}
		m.mu.Lock()
		m.tagCount = append(m.tagCount, n)
		m.mu.Unlock()
	}
	return args.Name + "-id", args.Inputs, nil
}

func createRecord(t *testing.T, tagRecords bool) *recordMocks {
	t.Helper()
	mocks := &recordMocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		c := New("zone123", "test-project", "prd", "use1", tagRecords, nil)
		spec := types.DnsSpec{
			Name: "bridge", Container: "bridge", Provider: "cloudflare",
			Compute: "bridge-01", Subdomain: "bridge",
		}
		out := types.ComputeOutputs{PublicIP: pulumi.String("1.2.3.4").ToStringOutput()}
		return c.Create(ctx, spec, out)
	}, pulumi.WithMocks("inforge", "test", mocks))
	require.NoError(t, err)
	return mocks
}

// TestCreateRecordTagsEnabled confirms record tags are applied when the project
// opts in (Enterprise zones).
func TestCreateRecordTagsEnabled(t *testing.T) {
	mocks := createRecord(t, true)
	require.Len(t, mocks.tagCount, 1)
	assert.Positive(t, mocks.tagCount[0], "expected tags to be set when tagRecords=true")
}

// TestCreateRecordTagsDisabled confirms no tags are sent when tagRecords is off,
// so the create is accepted on non-Enterprise zones (Cloudflare error 9300).
func TestCreateRecordTagsDisabled(t *testing.T) {
	mocks := createRecord(t, false)
	require.Len(t, mocks.tagCount, 1)
	assert.Equal(t, 0, mocks.tagCount[0], "expected no tags when tagRecords=false")
}
