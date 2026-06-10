package output

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/stretchr/testify/assert"
)

func TestShortType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hcloud:index/network:Network", "hcloud:Network"},
		{"cloudflare:index/record:Record", "cloudflare:Record"},
		{"neon:resources:NeonProject", "neon:NeonProject"},
		{"pulumi:pulumi:Stack", "pulumi:Stack"},
		{"nocodon", "nocodon"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, shortType(c.in), c.in)
	}
}

func TestUrnName(t *testing.T) {
	urn := "urn:pulumi:prd::wardnet-infrastructure::hcloud:index/network:Network::ingress-us-east-1"
	assert.Equal(t, "ingress-us-east-1", urnName(urn))
	assert.Equal(t, "bare", urnName("bare"))
}

func TestIsSystemResource(t *testing.T) {
	assert.True(t, isSystemResource("pulumi:pulumi:Stack"))
	assert.True(t, isSystemResource("pulumi:providers:hcloud"))
	assert.False(t, isSystemResource("hcloud:index/network:Network"))
}

func TestNewEventChannelIsBuffered(t *testing.T) {
	ch := NewEventChannel()
	assert.Equal(t, eventBufSize, cap(ch))
}

// engineEvent wraps an apitype event in the auto.events envelope Handle expects.
func engineEvent(e apitype.EngineEvent) events.EngineEvent {
	return events.EngineEvent{EngineEvent: e}
}

func handleAll(p *Printer, evs ...apitype.EngineEvent) {
	for _, e := range evs {
		p.Handle(engineEvent(e))
	}
}

func TestHandleResourcePreEvent(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p, apitype.EngineEvent{
		ResourcePreEvent: &apitype.ResourcePreEvent{
			Metadata: apitype.StepEventMetadata{
				Op:   apitype.OpCreate,
				Type: "hcloud:index/network:Network",
				URN:  "urn:pulumi:prd::proj::hcloud:index/network:Network::ingress-01",
			},
		},
	})
	out := buf.String()
	assert.Contains(t, out, "+")
	assert.Contains(t, out, "hcloud:Network")
	assert.Contains(t, out, "ingress-01")
}

func TestHandleDiagnosticError(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p, apitype.EngineEvent{
		DiagnosticEvent: &apitype.DiagnosticEvent{
			Severity: "error",
			Message:  "label value 'x' is not correctly formatted",
		},
	})
	assert.Contains(t, buf.String(), "error: label value")
}

func TestHandleDiagnosticInfoErrShown(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p, apitype.EngineEvent{
		DiagnosticEvent: &apitype.DiagnosticEvent{
			Severity: "info#err",
			Message:  "provider plugin requires update",
		},
	})
	assert.Contains(t, buf.String(), "provider plugin requires update")
}

func TestHandleDiagnosticInfoSuppressed(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p, apitype.EngineEvent{
		DiagnosticEvent: &apitype.DiagnosticEvent{
			Severity: "info",
			Message:  "verbose resource property output",
		},
	})
	assert.Empty(t, buf.String())
}

func TestHandleStdoutEvent(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p, apitype.EngineEvent{
		StdoutEvent: &apitype.StdoutEngineEvent{
			Message: "warning: plugin download failed, retrying\n",
		},
	})
	assert.Contains(t, buf.String(), "warning: plugin download failed, retrying")
}

func TestHandleSystemResourceSkipped(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p, apitype.EngineEvent{
		ResourcePreEvent: &apitype.ResourcePreEvent{
			Metadata: apitype.StepEventMetadata{
				Op:   apitype.OpCreate,
				Type: "pulumi:pulumi:Stack",
				URN:  "urn:pulumi:prd::proj::pulumi:pulumi:Stack::proj-prd",
			},
		},
	})
	assert.Empty(t, buf.String())
}

func TestHandleSameOpSkipped(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p, apitype.EngineEvent{
		ResourcePreEvent: &apitype.ResourcePreEvent{
			Metadata: apitype.StepEventMetadata{
				Op:   apitype.OpSame,
				Type: "hcloud:index/server:Server",
				URN:  "urn:pulumi:prd::proj::hcloud:index/server:Server::bridge-01",
			},
		},
	})
	assert.Empty(t, buf.String())
}

// TestFinishSummaryOrder: the applied-change counts print in create→update→delete
// order with past-tense labels, and OpSame is excluded.
func TestFinishSummaryOrder(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p, apitype.EngineEvent{
		SummaryEvent: &apitype.SummaryEvent{
			ResourceChanges: map[apitype.OpType]int{
				apitype.OpDelete: 1,
				apitype.OpCreate: 3,
				apitype.OpUpdate: 2,
				apitype.OpSame:   9,
			},
		},
	})
	p.Finish()
	out := buf.String()

	assert.Contains(t, out, "3 created")
	assert.Contains(t, out, "2 updated")
	assert.Contains(t, out, "1 deleted")
	assert.NotContains(t, out, "same")

	createPos := strings.Index(out, "created")
	updatePos := strings.Index(out, "updated")
	deletePos := strings.Index(out, "deleted")
	assert.Greater(t, updatePos, createPos, "update should come after create")
	assert.Greater(t, deletePos, updatePos, "delete should come after update")
}

// TestFinishPreviewTense: a preview summary uses future tense.
func TestFinishPreviewTense(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p, apitype.EngineEvent{
		SummaryEvent: &apitype.SummaryEvent{
			IsPreview:       true,
			ResourceChanges: map[apitype.OpType]int{apitype.OpCreate: 2},
		},
	})
	p.Finish()
	assert.Contains(t, buf.String(), "2 to create")
}

// TestFinishNoChanges: an empty summary with no failures reports no changes.
func TestFinishNoChanges(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p, apitype.EngineEvent{SummaryEvent: &apitype.SummaryEvent{}})
	p.Finish()
	assert.Contains(t, buf.String(), "no changes")
}

// TestFailureCapturedWithMessage: a ResOpFailedEvent records a Failure, and the
// preceding error DiagnosticEvent (same URN) supplies its message. Finish prints
// the failed count, the failure, and the abort banner.
func TestFailureCapturedWithMessage(t *testing.T) {
	const urn = "urn:pulumi:prd::proj::infisical:resources:InfisicalIdentity::wardnet-prd-use1-identity-bridge"
	var buf strings.Builder
	p := NewPrinter(&buf)
	handleAll(p,
		apitype.EngineEvent{
			DiagnosticEvent: &apitype.DiagnosticEvent{
				Severity: "error",
				URN:      urn,
				Message:  "infisical: list identity memberships failed (HTTP 404)\nsecond line ignored",
			},
		},
		apitype.EngineEvent{
			ResOpFailedEvent: &apitype.ResOpFailedEvent{
				Metadata: apitype.StepEventMetadata{
					Op:   apitype.OpCreate,
					Type: "infisical:resources:InfisicalIdentity",
					URN:  urn,
				},
			},
		},
		apitype.EngineEvent{
			SummaryEvent: &apitype.SummaryEvent{
				ResourceChanges: map[apitype.OpType]int{apitype.OpCreate: 14},
			},
		},
	)

	fails := p.Failures()
	assert.Len(t, fails, 1)
	assert.Equal(t, "infisical:InfisicalIdentity", fails[0].Type)
	assert.Equal(t, "wardnet-prd-use1-identity-bridge", fails[0].Name)
	assert.Equal(t, "create", fails[0].Op)
	assert.Equal(t, "infisical: list identity memberships failed (HTTP 404)", fails[0].Message,
		"only the first line of the diagnostic is captured for the compact failure list")

	p.Finish()
	out := buf.String()
	assert.Contains(t, out, "14 created")
	assert.Contains(t, out, "1 failed")
	assert.Contains(t, out, "wardnet-prd-use1-identity-bridge")
	assert.Contains(t, out, "did not complete", "abort banner must be present on failure")
}

// TestCountsFromOutputsWithoutSummary is the failure-path guard: when a run
// aborts and no final SummaryEvent arrives, the per-op counts must still come
// from the ResOutputsEvents of the resources that did complete — otherwise a
// failed deploy would report zero created. OpSame and system resources are
// excluded from the tally.
func TestCountsFromOutputsWithoutSummary(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)

	outputs := func(op apitype.OpType, typ, name string) apitype.EngineEvent {
		return apitype.EngineEvent{ResOutputsEvent: &apitype.ResOutputsEvent{
			Metadata: apitype.StepEventMetadata{Op: op, Type: typ, URN: "urn::" + name},
		}}
	}
	handleAll(p,
		outputs(apitype.OpCreate, "hcloud:index/network:Network", "net"),
		outputs(apitype.OpCreate, "hcloud:index/server:Server", "vm"),
		outputs(apitype.OpSame, "hcloud:index/server:Server", "unchanged"),       // excluded
		outputs(apitype.OpCreate, "pulumi:providers:hcloud", "provider"),         // excluded (system)
	)
	// No SummaryEvent — simulating an aborted run.

	assert.Equal(t, map[string]int{"create": 2}, p.Changes(),
		"counts must come from ResOutputsEvent when no SummaryEvent is emitted")

	p.Finish()
	assert.Contains(t, buf.String(), "2 created")
}

// TestChanges exposes the structured counts used by the JSON summary, excluding
// OpSame and zero entries.
func TestChanges(t *testing.T) {
	p := NewPrinter(&strings.Builder{})
	assert.Nil(t, p.Changes(), "no summary yet -> nil")
	handleAll(p, apitype.EngineEvent{
		SummaryEvent: &apitype.SummaryEvent{
			ResourceChanges: map[apitype.OpType]int{
				apitype.OpCreate: 14,
				apitype.OpSame:   3,
				apitype.OpUpdate: 0,
			},
		},
	})
	assert.Equal(t, map[string]int{"create": 14}, p.Changes())
}

// TestErrorEnvelope: a transport-level error on the events envelope is printed.
func TestErrorEnvelope(t *testing.T) {
	var buf strings.Builder
	p := NewPrinter(&buf)
	p.Handle(events.EngineEvent{Error: assertErr{}})
	assert.Contains(t, buf.String(), "error: boom")
}

type assertErr struct{}

func (assertErr) Error() string { return "boom" }
