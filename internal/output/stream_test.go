package output

import (
	"strings"
	"testing"

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

func TestStreamResourcePreEvent(t *testing.T) {
	ch := make(chan apitype.EngineEvent, 1)
	ch <- apitype.EngineEvent{
		ResourcePreEvent: &apitype.ResourcePreEvent{
			Metadata: apitype.StepEventMetadata{
				Op:   apitype.OpCreate,
				Type: "hcloud:index/network:Network",
				URN:  "urn:pulumi:prd::proj::hcloud:index/network:Network::ingress-01",
			},
		},
	}
	close(ch)

	var buf strings.Builder
	for ev := range ch {
		dispatch(ev, &buf)
	}

	out := buf.String()
	assert.Contains(t, out, "+")
	assert.Contains(t, out, "hcloud:Network")
	assert.Contains(t, out, "ingress-01")
}

func TestStreamDiagnosticError(t *testing.T) {
	ch := make(chan apitype.EngineEvent, 1)
	ch <- apitype.EngineEvent{
		DiagnosticEvent: &apitype.DiagnosticEvent{
			Severity: "error",
			Message:  "label value 'x' is not correctly formatted",
		},
	}
	close(ch)

	var buf strings.Builder
	for ev := range ch {
		dispatch(ev, &buf)
	}

	assert.Contains(t, buf.String(), "error: label value")
}

func TestStreamDiagnosticInfoErrShown(t *testing.T) {
	ch := make(chan apitype.EngineEvent, 1)
	ch <- apitype.EngineEvent{
		DiagnosticEvent: &apitype.DiagnosticEvent{
			Severity: "info#err",
			Message:  "provider plugin requires update",
		},
	}
	close(ch)

	var buf strings.Builder
	for ev := range ch {
		dispatch(ev, &buf)
	}

	assert.Contains(t, buf.String(), "provider plugin requires update")
}

func TestStreamDiagnosticInfoSuppressed(t *testing.T) {
	ch := make(chan apitype.EngineEvent, 1)
	ch <- apitype.EngineEvent{
		DiagnosticEvent: &apitype.DiagnosticEvent{
			Severity: "info",
			Message:  "verbose resource property output",
		},
	}
	close(ch)

	var buf strings.Builder
	for ev := range ch {
		dispatch(ev, &buf)
	}

	assert.Empty(t, buf.String())
}

func TestStreamStdoutEvent(t *testing.T) {
	ch := make(chan apitype.EngineEvent, 1)
	ch <- apitype.EngineEvent{
		StdoutEvent: &apitype.StdoutEngineEvent{
			Message: "warning: plugin download failed, retrying\n",
		},
	}
	close(ch)

	var buf strings.Builder
	for ev := range ch {
		dispatch(ev, &buf)
	}

	assert.Contains(t, buf.String(), "warning: plugin download failed, retrying")
}

func TestStreamSystemResourceSkipped(t *testing.T) {
	ch := make(chan apitype.EngineEvent, 1)
	ch <- apitype.EngineEvent{
		ResourcePreEvent: &apitype.ResourcePreEvent{
			Metadata: apitype.StepEventMetadata{
				Op:   apitype.OpCreate,
				Type: "pulumi:pulumi:Stack",
				URN:  "urn:pulumi:prd::proj::pulumi:pulumi:Stack::proj-prd",
			},
		},
	}
	close(ch)

	var buf strings.Builder
	for ev := range ch {
		dispatch(ev, &buf)
	}

	assert.Empty(t, buf.String())
}

func TestStreamSameOpSkipped(t *testing.T) {
	ch := make(chan apitype.EngineEvent, 1)
	ch <- apitype.EngineEvent{
		ResourcePreEvent: &apitype.ResourcePreEvent{
			Metadata: apitype.StepEventMetadata{
				Op:   apitype.OpSame,
				Type: "hcloud:index/server:Server",
				URN:  "urn:pulumi:prd::proj::hcloud:index/server:Server::bridge-01",
			},
		},
	}
	close(ch)

	var buf strings.Builder
	for ev := range ch {
		dispatch(ev, &buf)
	}

	assert.Empty(t, buf.String())
}

func TestPrintSummaryOrder(t *testing.T) {
	sum := &apitype.SummaryEvent{
		ResourceChanges: map[apitype.OpType]int{
			apitype.OpDelete: 1,
			apitype.OpCreate: 3,
			apitype.OpUpdate: 2,
		},
	}
	var buf strings.Builder
	printSummary(sum, &buf)
	out := buf.String()

	createPos := strings.Index(out, "to create")
	updatePos := strings.Index(out, "to update")
	deletePos := strings.Index(out, "to delete")
	assert.Greater(t, updatePos, createPos, "update should come after create")
	assert.Greater(t, deletePos, updatePos, "delete should come after update")
}
