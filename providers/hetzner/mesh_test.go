package hetzner

import (
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/meshnginx"
	"github.com/wardnet/inforge/internal/types"
)

// meshMocks records every registered command.remote by logical name, so a test can tell
// WHICH of the four commands Realize handed back.
type meshMocks struct {
	mu   sync.Mutex
	urns map[string]string // logical name -> URN
}

func newMeshMocks() *meshMocks { return &meshMocks{urns: map[string]string{}} }

func (m *meshMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func (m *meshMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	urn := "urn:pulumi:stack::project::" + args.TypeToken + "::" + args.Name
	m.mu.Lock()
	m.urns[args.Name] = urn
	m.mu.Unlock()
	return args.Name + "-id", args.Inputs, nil
}

// TestRealizeReturnsTheReloadNotTheConfigWrite is the contract the whole #226 fix rests on.
//
// Realize registers four commands: install, unit, config, reload. A service restart is
// ordered against whatever it returns — so returning the CONFIG WRITE instead of the RELOAD
// would look right, compile, pass every other test, and still leave the race wide open: the
// config write only puts bytes on disk, and the allow-map is not live until nginx reloads.
//
// Nothing but this test distinguishes those two.
func TestRealizeReturnsTheReloadNotTheConfigWrite(t *testing.T) {
	mocks := newMeshMocks()
	var got pulumi.Resource

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		host := types.ComputeOutputs{PublicIP: pulumi.String("1.2.3.4").ToStringOutput()}
		h := NewMesh("a-deploy-key", "use1")
		var err error
		got, err = h.Realize(ctx, "edge-01", host, "deploy", meshnginx.Config{
			TrustBundlePath: "/run/wardnet/mesh/bundle.crt",
		}, pulumi.String("10.0.0.2"), nil, "prd", nil)
		return err
	}, pulumi.WithMocks("project", "stack", mocks))
	require.NoError(t, err)

	require.NotNil(t, got, "a realized mesh must hand back the resource a restart can wait on")

	// All four commands registered, so the name below is a real choice among them.
	for _, suffix := range []string{"-install", "-unit", "-config", "-reload"} {
		found := false
		for name := range mocks.urns {
			if strings.HasSuffix(name, suffix) {
				found = true
			}
		}
		assert.True(t, found, "expected a %s command", suffix)
	}

	assert.True(t, strings.HasSuffix(urn(t, got), "-reload"),
		"Realize must return the RELOAD (what makes the allow-map live), not the config write (bytes on disk); got %s", urn(t, got))
}

// urn resolves a resource's URN under the mock engine. ApplyT is asynchronous, so the test
// must WAIT for it — an unsynchronised read races the engine and silently yields "", which
// makes every HasSuffix assertion vacuously pass.
func urn(t *testing.T, r pulumi.Resource) string {
	t.Helper()
	var got string
	var wg sync.WaitGroup
	wg.Add(1)
	r.URN().ApplyT(func(u pulumi.URN) string {
		got = string(u)
		wg.Done()
		return string(u)
	})
	wg.Wait()
	require.NotEmpty(t, got, "URN did not resolve")
	return got
}
