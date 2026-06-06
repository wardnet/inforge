package bootstrapper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock advances its own Now only when Sleep is called, so backoff tests are
// deterministic and never actually wait.
type fakeClock struct {
	now   time.Time
	slept []time.Duration
	total time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) {
	c.slept = append(c.slept, d)
	c.total += d
	c.now = c.now.Add(d)
}

// fakeFetcher returns its scripted results in order, looping on the last entry.
type fakeFetcher struct {
	results []fetchResult
	calls   int
}

type fetchResult struct {
	secrets map[string]string
	err     error
}

func (f *fakeFetcher) Fetch(context.Context) (map[string]string, error) {
	i := f.calls
	if i >= len(f.results) {
		i = len(f.results) - 1
	}
	f.calls++
	return f.results[i].secrets, f.results[i].err
}

func TestFetchWithBackoffSucceedsAfterTransient(t *testing.T) {
	want := map[string]string{"infra/DB": "x"}
	f := &fakeFetcher{results: []fetchResult{
		{err: transient("net blip")},
		{err: transient("HTTP 503")},
		{secrets: want},
	}}
	clk := &fakeClock{now: time.Unix(0, 0)}

	got, err := FetchWithBackoff(context.Background(), f, clk, time.Second, time.Minute, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 3, f.calls)
	// Exponential: 1s then 2s before the third (successful) attempt.
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, clk.slept)
}

// TestFetchWithBackoffFailsFastOnPermanent: a permanent error (e.g. 401/403)
// returns immediately with no retries.
func TestFetchWithBackoffFailsFastOnPermanent(t *testing.T) {
	permanent := errors.New("infisical: authenticate failed (HTTP 401)")
	f := &fakeFetcher{results: []fetchResult{{err: permanent}}}
	clk := &fakeClock{now: time.Unix(0, 0)}

	_, err := FetchWithBackoff(context.Background(), f, clk, time.Second, time.Minute, 5*time.Minute)
	require.Error(t, err)
	assert.ErrorIs(t, err, permanent)
	assert.Equal(t, 1, f.calls, "permanent error must not retry")
	assert.Empty(t, clk.slept)
}

// TestFetchWithBackoffGivesUpAfterBudget: an always-transient provider is retried
// until the budget elapses, then returns the last error (so the start exits
// non-zero and systemd restarts).
func TestFetchWithBackoffGivesUpAfterBudget(t *testing.T) {
	f := &fakeFetcher{results: []fetchResult{{err: transient("always down")}}}
	clk := &fakeClock{now: time.Unix(0, 0)}

	_, err := FetchWithBackoff(context.Background(), f, clk, time.Second, 60*time.Second, 5*time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "giving up")
	assert.GreaterOrEqual(t, clk.total, 5*time.Minute)
	// Delay is capped at maxDelay (60s).
	for _, d := range clk.slept {
		assert.LessOrEqual(t, d, 60*time.Second)
	}
}

// TestFetchWithBackoffRespectsContext: a cancelled context returns promptly.
func TestFetchWithBackoffRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeFetcher{results: []fetchResult{{err: transient("down")}}}
	clk := &fakeClock{now: time.Unix(0, 0)}

	_, err := FetchWithBackoff(ctx, f, clk, time.Second, time.Minute, 5*time.Minute)
	assert.ErrorIs(t, err, context.Canceled)
}
