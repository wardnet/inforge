package main

import (
	"context"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/types"
)

// fakeConfigSetter records the stack config setDerivedStackConfig writes, standing
// in for a real auto.Stack (which needs a Pulumi workspace). It also counts calls:
// every derived key must land in ONE set-all, never a round-trip per key.
type fakeConfigSetter struct {
	set   map[string]string
	calls int
}

func newFakeConfigSetter() *fakeConfigSetter {
	return &fakeConfigSetter{set: map[string]string{}}
}

func (f *fakeConfigSetter) SetAllConfig(_ context.Context, cfg auto.ConfigMap) error {
	f.calls++
	for k, v := range cfg {
		f.set[k] = v.Value
	}
	return nil
}

func TestSetDerivedStackConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("writes every derived key in one round-trip", func(t *testing.T) {
		t.Setenv("CLOUDFLARE_ACCOUNT_ID", "abc123")
		f := newFakeConfigSetter()

		require.NoError(t, setDerivedStackConfig(ctx, f, "./other-tree", projectConfig{
			Providers: types.ProviderDefaults{
				Compute:  "hetzner",
				Database: map[string]string{"postgresql": "self-hosted"},
			},
			Backups: backupsConfig{Bucket: "wardnet-backups"},
		}))

		assert.Equal(t, 1, f.calls, "the derived keys must be written in a single set-all")
		assert.Equal(t, "./other-tree", f.set[cfgKeyDir])
		assert.JSONEq(t, `{"Compute":"hetzner","Database":{"postgresql":"self-hosted"}}`, f.set[cfgKeyProviderDefaults])
		assert.Equal(t, "wardnet-backups", f.set[cfgKeyBackupsBucket])
		assert.Contains(t, f.set[cfgKeyBackupsEndpoint], "abc123")
	})

	// Stack config persists across runs, so an unconfigured block must CLEAR the
	// previous run's value rather than silently keep applying it. Every key is
	// written even when nothing is configured; program.Run decodes each zero value
	// back to "not configured".
	t.Run("an unconfigured project still writes every key", func(t *testing.T) {
		f := newFakeConfigSetter()

		require.NoError(t, setDerivedStackConfig(ctx, f, "", projectConfig{}))

		assert.Equal(t, 1, f.calls)
		// No --dir given: the default tree, written anyway so an earlier run's --dir
		// cannot persist into a run that did not ask for it.
		assert.Equal(t, defaultResourcesDir, f.set[cfgKeyDir])

		v, ok := f.set[cfgKeyProviderDefaults]
		require.True(t, ok, "provider_defaults must be written even when unconfigured")
		assert.JSONEq(t, `{"Compute":"","Database":null}`, v)

		bucket, ok := f.set[cfgKeyBackupsBucket]
		require.True(t, ok, "backups_bucket must be written even when unconfigured")
		assert.Empty(t, bucket)

		endpoint, ok := f.set[cfgKeyBackupsEndpoint]
		require.True(t, ok, "backups_endpoint must be written even when unconfigured")
		assert.Empty(t, endpoint)
	})

	// A configured bucket whose endpoint cannot be resolved fails the command up
	// front, rather than writing a half-derived config and failing mid-apply on the
	// host.
	t.Run("a backups bucket with no account id fails and writes nothing", func(t *testing.T) {
		t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
		f := newFakeConfigSetter()

		err := setDerivedStackConfig(ctx, f, "./resources", projectConfig{
			Backups: backupsConfig{Bucket: "wardnet-backups"},
		})

		require.Error(t, err)
		assert.Zero(t, f.calls, "no stack config may be written when the backup destination cannot be resolved")
	})
}
