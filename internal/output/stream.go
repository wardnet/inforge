// Package output formats Pulumi engine events into structured human-readable
// output for the inforge CLI. It is used in place of ProgressStreams so that
// resource operations, failures, and an end-of-run summary are presented
// clearly without the full Pulumi progress tree (and without the duplication
// that results from pointing ProgressStreams and ErrorProgressStreams at the
// same writer).
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

// Failure describes a resource whose create/update/delete operation failed. It
// is also serialised into the --output json summary so the deploy workflow can
// report failures, not just success counts.
type Failure struct {
	Op      string `json:"op"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Message string `json:"message,omitempty"`
}

// Printer formats Pulumi engine events into human-readable output and records
// the run outcome — per-op change counts (from the engine's SummaryEvent) and
// per-resource failures — so it can print a consolidated end-of-run summary and
// expose the structured failure list.
//
// A Printer consumes exactly one run's event stream. Its methods are not safe
// for concurrent use: drive Handle from a single goroutine, and call Finish,
// Changes, or Failures only after that goroutine has drained the channel.
type Printer struct {
	w       io.Writer
	summary *apitype.SummaryEvent
	// completed tallies finished operations from ResOutputsEvent as they arrive
	// (OpSame and system resources excluded). This is the authoritative count of
	// what actually completed: it is emitted per resource regardless of whether
	// the run reaches a final SummaryEvent, so a run that aborts mid-way still
	// reports the resources that did succeed. SummaryEvent is used only as a
	// fallback when no ResOutputsEvents were seen (e.g. a preview that emits a
	// summary but no per-resource outputs).
	completed map[apitype.OpType]int
	failures  []Failure
	// errByURN holds the most recent error diagnostic message per resource URN.
	// A ResOpFailedEvent carries no message of its own — the root cause arrives
	// just before it as a DiagnosticEvent — so we stash it here and attach it
	// when the failure event lands.
	errByURN map[string]string
	// destructive collects, per delete/delete-replaced operation, what the
	// delete destroys on a host (from the resource-name grammar). Finish prints
	// them as their own section: a deploy must never destroy host state only a
	// resource-name connoisseur can spot in the stream.
	destructive []string
}

// NewPrinter returns a Printer that writes formatted output to w.
func NewPrinter(w io.Writer) *Printer {
	return &Printer{
		w:         w,
		completed: map[apitype.OpType]int{},
		errByURN:  map[string]string{},
	}
}

// Stream drives a Printer over ch until it is closed, then prints the summary.
// Retained for callers that only need the rendered output and not the
// structured change counts / failure list.
func Stream(ch <-chan events.EngineEvent, w io.Writer) {
	p := NewPrinter(w)
	for ev := range ch {
		p.Handle(ev)
	}
	p.Finish()
}

// Handle processes a single engine event: it prints per-resource progress and
// inline diagnostics, and accumulates the success/failure tallies Finish prints.
func (p *Printer) Handle(ev events.EngineEvent) {
	if ev.Error != nil {
		_, _ = fmt.Fprintf(p.w, "  error: %v\n", ev.Error)
		return
	}
	e := ev.EngineEvent
	switch {
	case e.ResourcePreEvent != nil:
		m := e.ResourcePreEvent.Metadata
		if isSystemResource(m.Type) || m.Op == apitype.OpSame {
			return
		}
		info := translate(m.Type, urnName(m.URN))
		warn := ""
		isDelete := m.Op == apitype.OpDelete || (m.Op == apitype.OpDeleteReplaced && !info.suppressOnReplace)
		if isDelete && info.destroys != "" {
			warn = "  ⚠"
			p.destructive = append(p.destructive, fmt.Sprintf("%s: %s", info.display(), info.destroys))
		}
		_, _ = fmt.Fprintf(p.w, "  %-2s %-44s %s%s%s\n",
			opSymbol(m.Op), info.display(), opVerb(m.Op), diffSuffix(m), warn)

	case e.ResOutputsEvent != nil:
		m := e.ResOutputsEvent.Metadata
		if isSystemResource(m.Type) || m.Op == apitype.OpSame {
			return
		}
		p.completed[m.Op]++

	case e.ResOpFailedEvent != nil:
		m := e.ResOpFailedEvent.Metadata
		if isSystemResource(m.Type) {
			return
		}
		p.failures = append(p.failures, Failure{
			Op:      string(m.Op),
			Type:    shortType(m.Type),
			Name:    urnName(m.URN),
			Message: p.errByURN[m.URN],
		})

	case e.DiagnosticEvent != nil:
		d := e.DiagnosticEvent
		if d.Ephemeral {
			return
		}
		switch d.Severity {
		case "error", "warning", "info#err":
			msg := strings.TrimSpace(d.Message)
			if msg == "" {
				return
			}
			if d.Severity == "error" && d.URN != "" {
				p.errByURN[d.URN] = firstLine(msg)
			}
			_, _ = fmt.Fprintf(p.w, "      %s: %s\n", d.Severity, msg)
		}

	case e.StdoutEvent != nil:
		// StdoutEvent carries generic CLI-level messages from the Pulumi engine
		// (plugin notices, auth warnings, deprecation notices). Print them so
		// they are not silently lost.
		if msg := strings.TrimSpace(e.StdoutEvent.Message); msg != "" {
			_, _ = fmt.Fprintf(p.w, "  %s\n", msg)
		}

	case e.SummaryEvent != nil:
		p.summary = e.SummaryEvent
	}
}

// effectiveChanges returns the per-op counts to report: the live ResOutputsEvent
// tally when any operation completed, otherwise the SummaryEvent's counts (with
// OpSame and zero entries dropped). Preferring the live tally is what makes the
// counts correct on an aborted run, where a final SummaryEvent may never arrive.
func (p *Printer) effectiveChanges() map[apitype.OpType]int {
	if len(p.completed) > 0 {
		return p.completed
	}
	if p.summary == nil {
		return nil
	}
	out := make(map[apitype.OpType]int, len(p.summary.ResourceChanges))
	for op, n := range p.summary.ResourceChanges {
		if op == apitype.OpSame || n == 0 {
			continue
		}
		out[op] = n
	}
	return out
}

// Changes returns the per-op change counts (keyed by OpType string) for the
// JSON summary, or nil if nothing changed. OpSame is omitted. See
// effectiveChanges for the source.
func (p *Printer) Changes() map[string]int {
	changes := p.effectiveChanges()
	if len(changes) == 0 {
		return nil
	}
	out := make(map[string]int, len(changes))
	for op, n := range changes {
		out[string(op)] = n
	}
	return out
}

// Failures returns the resources whose operation failed during the run.
func (p *Printer) Failures() []Failure { return p.failures }

// Destructive returns the destructive-operation descriptions collected during
// the run — the same entries the terminal ⚠ section shows, exposed so the
// markdown report (the artifact a PR reviewer actually reads) carries them too.
func (p *Printer) Destructive() []string { return p.destructive }

// abortBanner explains, after a failed run, that absence from the change counts
// does not mean a resource is healthy — Pulumi stops a dependency chain at the
// first error, so dependents of a failed resource are skipped, not applied.
const abortBanner = "Update did not complete: the resources above failed, and any resources that\n" +
	"depend on them were skipped (not created or updated). A resource missing from\n" +
	"the counts above was not necessarily applied — fix the errors and re-run."

// Finish prints the consolidated end-of-run summary: the per-op change counts,
// a failed count and per-resource failure list (with the captured error), and —
// when anything failed — the abort banner. Call exactly once, after the event
// stream has been fully drained.
func (p *Printer) Finish() {
	preview := p.summary != nil && p.summary.IsPreview
	changes := p.effectiveChanges()

	if len(p.destructive) > 0 {
		_, _ = fmt.Fprintln(p.w)
		if preview {
			_, _ = fmt.Fprintln(p.w, "⚠ Destructive operations in this plan:")
		} else {
			_, _ = fmt.Fprintln(p.w, "⚠ Destructive operations in this run:")
		}
		for _, d := range p.destructive {
			_, _ = fmt.Fprintf(p.w, "  ⚠ %s\n", d)
		}
	}

	_, _ = fmt.Fprintln(p.w)
	_, _ = fmt.Fprintln(p.w, "Summary:")

	// Fixed priority order (create → update → delete → replace) so the output is
	// stable; any unknown future OpType prints last but consistently.
	total := 0
	seen := map[apitype.OpType]bool{}
	ordered := []apitype.OpType{
		apitype.OpCreate, apitype.OpUpdate, apitype.OpDelete,
		apitype.OpCreateReplacement, apitype.OpDeleteReplaced,
	}
	for _, op := range ordered {
		if n := changes[op]; n > 0 {
			_, _ = fmt.Fprintf(p.w, "  %s %d %s\n", opSymbol(op), n, opLabel(op, preview))
			seen[op] = true
			total += n
		}
	}
	for op, n := range changes {
		if !seen[op] && op != apitype.OpSame && n > 0 {
			_, _ = fmt.Fprintf(p.w, "  %s %d %s\n", opSymbol(op), n, opLabel(op, preview))
			total += n
		}
	}
	if total == 0 && len(p.failures) == 0 {
		_, _ = fmt.Fprintln(p.w, "  no changes")
	}

	if len(p.failures) == 0 {
		return
	}
	_, _ = fmt.Fprintf(p.w, "  ✗ %d failed\n\n", len(p.failures))
	_, _ = fmt.Fprintln(p.w, "Failed:")
	for _, f := range p.failures {
		_, _ = fmt.Fprintf(p.w, "  ✗ %-36s  %s\n", f.Type, f.Name)
		if f.Message != "" {
			_, _ = fmt.Fprintf(p.w, "      %s\n", f.Message)
		}
	}
	_, _ = fmt.Fprintln(p.w)
	_, _ = fmt.Fprintln(p.w, abortBanner)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// opWords is the single op→wording table: the stream symbol, the tense-neutral
// in-stream verb, and the summary's future/past labels. One table so a newly
// supported op (an import, a refresh) cannot be added to one renderer and
// missed in another.
var opWords = map[apitype.OpType]struct {
	symbol, verb, future, past string
}{
	apitype.OpCreate:            {"+", "create", "to create", "created"},
	apitype.OpUpdate:            {"~", "update", "to update", "updated"},
	apitype.OpDelete:            {"-", "delete", "to delete", "deleted"},
	apitype.OpCreateReplacement: {"+-", "replace", "to replace", "replaced"},
	apitype.OpDeleteReplaced:    {"+-", "delete (for replace)", "to delete (replaced)", "deleted (replaced)"},
}

func opSymbol(op apitype.OpType) string {
	if w, ok := opWords[op]; ok {
		return w.symbol
	}
	return "?"
}

// opVerb is the tense-neutral verb for a resource's in-stream line ("what is
// being done"), distinct from opLabel's summary tenses.
func opVerb(op apitype.OpType) string {
	if w, ok := opWords[op]; ok {
		return w.verb
	}
	return string(op)
}

// opLabel renders an OpType for the summary. preview selects future tense
// ("to create") for `inforge preview`; an applied update reads past tense
// ("created").
func opLabel(op apitype.OpType, preview bool) string {
	w, ok := opWords[op]
	if !ok {
		return string(op)
	}
	if preview {
		return w.future
	}
	return w.past
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
