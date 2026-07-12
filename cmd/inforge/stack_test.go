package main

import (
	"context"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
)

// fakeConfigSetter records the stack config keys the injectors write, standing in
// for a real auto.Stack (which needs a Pulumi workspace).
type fakeConfigSetter struct {
	set map[string]string
}

func newFakeConfigSetter() *fakeConfigSetter {
	return &fakeConfigSetter{set: map[string]string{}}
}

func (f *fakeConfigSetter) SetConfig(_ context.Context, key string, val auto.ConfigValue) error {
	f.set[key] = val.Value
	return nil
}

func TestSetProviderDefaultsAlwaysWritesTheKey(t *testing.T) {
	ctx := context.Background()

	// Configured defaults are marshalled through.
	f := newFakeConfigSetter()
	require.NoError(t, setProviderDefaults(ctx, f, types.ProviderDefaults{
		Compute:  "hetzner",
		Database: map[string]string{"postgresql": "self-hosted"},
	}))
	assert.JSONEq(t, `{"Compute":"hetzner","Database":{"postgresql":"self-hosted"}}`, f.set["provider_defaults"])

	// An empty `providers:` block still writes the key — stack config persists, so
	// removing the block must clear stale defaults rather than silently keep them.
	f = newFakeConfigSetter()
	require.NoError(t, setProviderDefaults(ctx, f, types.ProviderDefaults{}))
	v, ok := f.set["provider_defaults"]
	require.True(t, ok, "provider_defaults must be written even when unconfigured")
	assert.JSONEq(t, `{"Compute":"","Database":null}`, v)
}

func TestSetResourcesDir(t *testing.T) {
	ctx := context.Background()

	f := newFakeConfigSetter()
	require.NoError(t, setResourcesDir(ctx, f, "./other-tree"))
	assert.Equal(t, "./other-tree", f.set["dir"])

	// Unset falls back to the default tree, and is written anyway so an earlier
	// run's --dir cannot persist into a run that did not ask for it.
	f = newFakeConfigSetter()
	require.NoError(t, setResourcesDir(ctx, f, ""))
	assert.Equal(t, defaultResourcesDir, f.set["dir"])
}
