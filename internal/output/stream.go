// Package output formats Pulumi engine events into structured human-readable
// output for the inforge CLI. It is used in place of ProgressStreams so that
// resource operations and errors are presented clearly without the full Pulumi
// progress tree.
package output

import (
	"fmt"
	"io"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

// eventBufSize is the number of events buffered in the channel returned by
// NewEventChannel. A non-zero buffer decouples the Pulumi engine's blocking
// event sends from the consumer goroutine, preventing the engine from stalling
// when the writer is slow (e.g. a CI log pipe or terminal).
const eventBufSize = 128

// NewEventChannel returns a buffered channel for use with
// optup.EventStreams / optpreview.EventStreams.
func NewEventChannel() chan events.EngineEvent {
	return make(chan events.EngineEvent, eventBufSize)
}

// Stream reads Pulumi engine events from ch and writes formatted output to w.
// It blocks until ch is closed. Intended to run in a goroutine alongside
// s.Up() or s.Preview().
func Stream(ch <-chan events.EngineEvent, w io.Writer) {
	for ev := range ch {
		if ev.Error != nil {
			fmt.Fprintf(w, "  error: %v\n", ev.Error)
			continue
		}
		dispatch(ev.EngineEvent, w)
	}
}

func dispatch(ev apitype.EngineEvent, w io.Writer) {
	switch {
	case ev.ResourcePreEvent != nil:
		pre := ev.ResourcePreEvent
		if isSystemResource(pre.Metadata.Type) || pre.Metadata.Op == apitype.OpSame {
			return
		}
		fmt.Fprintf(w, "  %s %-36s  %s\n",
			opSymbol(pre.Metadata.Op),
			shortType(pre.Metadata.Type),
			urnName(pre.Metadata.URN),
		)

	case ev.DiagnosticEvent != nil:
		diag := ev.DiagnosticEvent
		if diag.Ephemeral {
			return
		}
		switch diag.Severity {
		case "error", "warning", "info#err":
			msg := strings.TrimSpace(diag.Message)
			if msg != "" {
				fmt.Fprintf(w, "      %s: %s\n", diag.Severity, msg)
			}
		}

	case ev.StdoutEvent != nil:
		// StdoutEvent carries generic CLI-level messages from the Pulumi engine
		// (plugin notices, auth warnings, deprecation notices). Print them so
		// they are not silently lost.
		msg := strings.TrimSpace(ev.StdoutEvent.Message)
		if msg != "" {
			fmt.Fprintf(w, "  %s\n", msg)
		}

	case ev.SummaryEvent != nil:
		printSummary(ev.SummaryEvent, w)
	}
}

// opPriority defines the display order for the summary: creates first, then
// updates, deletions, and replacements. Unknown ops sort last.
var opPriority = map[apitype.OpType]int{
	apitype.OpCreate:            0,
	apitype.OpUpdate:            1,
	apitype.OpDelete:            2,
	apitype.OpCreateReplacement: 3,
	apitype.OpDeleteReplaced:    4,
}

func printSummary(sum *apitype.SummaryEvent, w io.Writer) {
	fmt.Fprintln(w)
	if len(sum.ResourceChanges) == 0 {
		fmt.Fprintln(w, "No changes.")
		return
	}

	// Print in semantic priority order (create → update → delete → replace).
	// Iterate the fixed priority list so unknown future OpTypes appear last but
	// consistently, regardless of their string value.
	seen := make(map[apitype.OpType]bool)
	ordered := []apitype.OpType{
		apitype.OpCreate,
		apitype.OpUpdate,
		apitype.OpDelete,
		apitype.OpCreateReplacement,
		apitype.OpDeleteReplaced,
	}
	for _, op := range ordered {
		if count, ok := sum.ResourceChanges[op]; ok && count > 0 {
			fmt.Fprintf(w, "  %d %s\n", count, opLabel(op))
			seen[op] = true
		}
	}
	// Any OpType not in the known list (future additions) falls through here.
	for op, count := range sum.ResourceChanges {
		if !seen[op] && op != apitype.OpSame && count > 0 {
			fmt.Fprintf(w, "  %d %s\n", count, opLabel(op))
		}
	}
}

func opSymbol(op apitype.OpType) string {
	switch op {
	case apitype.OpCreate:
		return "+"
	case apitype.OpUpdate:
		return "~"
	case apitype.OpDelete:
		return "-"
	case apitype.OpCreateReplacement, apitype.OpDeleteReplaced:
		return "+-"
	default:
		return "?"
	}
}

func opLabel(op apitype.OpType) string {
	switch op {
	case apitype.OpCreate:
		return "to create"
	case apitype.OpUpdate:
		return "to update"
	case apitype.OpDelete:
		return "to delete"
	case apitype.OpCreateReplacement:
		return "to replace"
	case apitype.OpDeleteReplaced:
		return "to delete (replaced)"
	default:
		return string(op)
	}
}

// isSystemResource returns true for Pulumi meta-resources (the stack itself
// and provider resources) that are not meaningful to the user.
func isSystemResource(t string) bool {
	return strings.HasPrefix(t, "pulumi:pulumi:") ||
		strings.HasPrefix(t, "pulumi:providers:")
}

// shortType formats a Pulumi resource type token for display.
// "hcloud:index/network:Network" -> "hcloud:Network"
func shortType(t string) string {
	parts := strings.SplitN(t, ":", 3)
	if len(parts) == 3 {
		return parts[0] + ":" + parts[2]
	}
	return t
}

// urnName extracts the resource name from a Pulumi URN.
// "urn:pulumi:env::project::type::name" -> "name"
func urnName(urn string) string {
	parts := strings.Split(urn, "::")
	if len(parts) >= 4 {
		return parts[len(parts)-1]
	}
	return urn
}
