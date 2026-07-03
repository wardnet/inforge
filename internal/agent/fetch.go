package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SecretsFetcher fetches a service's secrets from a provider. It returns a flat
// map keyed by the secret's path relative to the provider's secret_path joined
// with the key (e.g. "infra/DATABASE_URL", "custom/STRIPE_KEY") so the env
// mapping in the descriptor can resolve each var by that exact reference.
//
// A returned error wrapped as transient (see transientError) signals the backoff
// wrapper to retry; any other error is permanent and fails the start fast.
type SecretsFetcher interface {
	Fetch(ctx context.Context) (map[string]string, error)
}

// transientError marks an error as retryable (network blip, 5xx, 429). The
// provider impl owns the HTTP-to-transient classification; FetchWithBackoff owns
// the retry policy and inspects errors only via isTransient.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// transient wraps err (built from format/args) as retryable. Callers must never
// pass a secret value into the message — only status codes and the provider's
// own error text, never a fetched body.
func transient(format string, a ...any) error {
	return &transientError{err: fmt.Errorf(format, a...)}
}

func isTransient(err error) bool {
	var t *transientError
	return errors.As(err, &t)
}

// Clock abstracts time so backoff is deterministically testable. Sleep takes a
// context so a cancellation (systemd SIGTERM during a backoff delay) aborts the
// wait promptly instead of after the full delay.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Backoff policy for a single start attempt: a service start that can't reach
// the vault retries with bounded exponential backoff up to budget, then exits
// non-zero so systemd (Restart=on-failure, StartLimitIntervalSec=0) restarts and
// tries again — a vault outage delays start, it never permanently fails it.
const (
	baseDelay = 1 * time.Second
	maxDelay  = 60 * time.Second
	budget    = 5 * time.Minute
)

// FetchWithBackoff calls f.Fetch, retrying transient errors with bounded
// exponential backoff (base doubling up to maxDelay) until budget elapses, then
// returns the last error. Permanent errors (e.g. 401/403) return immediately.
func FetchWithBackoff(ctx context.Context, f SecretsFetcher, clk Clock, base, max, budget time.Duration) (map[string]string, error) {
	start := clk.Now()
	delay := base
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		secrets, err := f.Fetch(ctx)
		if err == nil {
			return secrets, nil
		}
		if !isTransient(err) {
			return nil, err
		}
		if clk.Now().Sub(start) >= budget {
			return nil, fmt.Errorf("giving up fetching secrets after %s: %w", budget, err)
		}
		clk.Sleep(ctx, delay)
		delay *= 2
		if delay > max {
			delay = max
		}
	}
}
