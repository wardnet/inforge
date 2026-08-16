package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuf is a goroutine-safe buffer: the Printer writes from the drain
// goroutine while the test's run func reads it to synchronize on the mute
// boundary (the mute happens when the drain goroutine PROCESSES the first
// event, not when it is sent — in production a line or two may straddle that
// boundary harmlessly; a test must not depend on the race).
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuf) waitFor(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(b.String(), substr) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in output:\n%s", substr, b.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// Anything the engine says BEFORE its first event — above all the DIY
// backend's lock-retry loop — must reach the terminal live: the alternative
// was a 30-minute "Deploying (prd): …silence" hang in CI. After the first
// event the Printer owns the output and the raw streams go quiet.
func TestStreamEngineRunRelaysPrePlanOutput(t *testing.T) {
	out := &syncBuf{}
	_, err := streamEngineRun(out, "Deploying (prd):\n", func(ch chan events.EngineEvent, progress, errProgress io.Writer) error {
		// Pre-plan: the engine narrates on both raw streams.
		_, _ = progress.Write([]byte("the stack is currently locked by 1 other update(s) — retrying\n"))
		_, _ = errProgress.Write([]byte("warning: something pre-plan\n"))
		// The first engine event mutes the relay.
		ch <- events.EngineEvent{EngineEvent: apitype.EngineEvent{
			ResourcePreEvent: &apitype.ResourcePreEvent{Metadata: apitype.StepEventMetadata{
				Op:   apitype.OpUpdate,
				Type: "command:remote:Command",
				URN:  "urn:pulumi:prd::proj::command:remote:Command::wardnet-prd-use1-svc-tenants-provision",
			}},
		}}
		// Wait until the drain goroutine has processed the event (the Printer
		// line is out) — the mute is then guaranteed in effect.
		out.waitFor(t, "service unit tenants")
		// Post-event raw output is swallowed (progress) / buffered (errors).
		_, _ = progress.Write([]byte("NOISY-PROGRESS-TREE\n"))
		_, _ = errProgress.Write([]byte("post-event stderr\n"))
		close(ch)
		return nil
	})
	require.NoError(t, err)
	got := out.String()

	assert.Contains(t, got, "currently locked", "pre-plan progress must stream live")
	assert.Contains(t, got, "something pre-plan", "pre-plan stderr must stream live")
	assert.NotContains(t, got, "NOISY-PROGRESS-TREE", "post-event progress is the Printer's job")
	assert.NotContains(t, got, "post-event stderr", "post-event errors buffer for the failure dump; the run succeeded")
	assert.Contains(t, got, "service unit tenants (use1)", "the Printer takes over at the first event")
}

// On a failed run with no per-resource failure, the buffered error stream is
// the only diagnostic — but the prefix already relayed live must not print
// twice, and the un-relayed remainder must not be lost.
func TestStreamEngineRunDumpsOnlyUnrelayedErrors(t *testing.T) {
	var out bytes.Buffer
	_, err := streamEngineRun(&out, "", func(ch chan events.EngineEvent, progress, errProgress io.Writer) error {
		_, _ = errProgress.Write([]byte("RELAYED-PREFIX\n"))
		ch <- events.EngineEvent{EngineEvent: apitype.EngineEvent{
			SummaryEvent: &apitype.SummaryEvent{},
		}}
		_, _ = errProgress.Write([]byte("BUFFERED-REMAINDER\n"))
		close(ch)
		return errors.New("config error before any op")
	})
	require.Error(t, err)
	got := out.String()

	assert.Equal(t, 1, strings.Count(got, "RELAYED-PREFIX"), "the live-relayed prefix must not print twice")
	assert.Contains(t, got, "BUFFERED-REMAINDER", "the un-relayed remainder is the failure diagnostic")
}

// A run that dies BEFORE the engine's event stream starts returns its error
// WITHOUT closing the event channel — the Automation API only closes it once the
// engine has completed. streamEngineRun must still return.
//
// This is the prd outage of 2026-08-14: the R2 state write was rejected ~1s in,
// the error was relayed live by preludeRelay, and then the drain goroutine sat on
// `range ch` forever while wg.Wait() blocked. `inforge deploy` never exited and
// burned the full 6h CI timeout every night, which disguised a one-second failure
// as a hang and sent the diagnosis after the wrong cause entirely.
func TestStreamEngineRunReturnsWhenEngineNeverClosesChannel(t *testing.T) {
	type result struct {
		err error
		out string
	}
	done := make(chan result, 1)

	go func() {
		var out bytes.Buffer
		_, err := streamEngineRun(&out, "Deploying (prd):\n", func(ch chan events.EngineEvent, progress, errProgress io.Writer) error {
			// The backend rejects the very first write; no engine event is ever
			// emitted and — the point of this test — ch is never closed.
			_, _ = errProgress.Write([]byte("error: operation error S3: PutObject, api error InvalidDigest\n"))
			return errors.New("operation error S3: PutObject: InvalidDigest")
		})
		done <- result{err: err, out: out.String()}
	}()

	select {
	case got := <-done:
		require.Error(t, got.err, "the run's error must be returned, not swallowed")
		assert.Contains(t, got.out, "InvalidDigest", "the engine's diagnostic must still reach the operator")
	case <-time.After(30 * time.Second):
		t.Fatal("streamEngineRun did not return when the engine left the event channel open — `inforge deploy` would hang until the CI timeout")
	}
}
